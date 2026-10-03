package deploy

import (
	"bytes"
	"context"
	"crypto/rand"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/agent/internal/certs/certstest"
	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/cron"
	"github.com/shipwick/shipwick/pkg/spec"
)

// newServer is a second server: a harness whose database is sealed under a
// key of its own.
func newServer(t *testing.T, key string) *harness {
	t.Helper()
	st, err := store.Open(context.Background(), ":memory:", store.Options{EncryptionKey: []byte(key)})
	if err != nil {
		t.Fatalf("open store: %v", err)
	}
	rt := dockertest.New()
	dns := newFakeResolver()
	engine := New(st, rt, Options{
		StabilizeWindow: 20 * time.Millisecond,
		NameSettle:      time.Millisecond,
		Logger:          slog.New(slog.NewTextHandler(io.Discard, nil)),
		LookupHost:      dns.LookupHost,
		ServerAddresses: []string{"203.0.113.77"},
	})
	t.Cleanup(func() {
		engine.Shutdown(context.Background())
		st.Close()
	})
	return &harness{t: t, store: st, rt: rt, dns: dns, engine: engine}
}

const otherKey = "another-encryption-key-32-bytes!"

func (h *harness) export(only ...string) []byte {
	h.t.Helper()
	var buf bytes.Buffer
	if err := h.engine.Export(context.Background(), &buf, only); err != nil {
		h.t.Fatalf("Export: %v", err)
	}
	return buf.Bytes()
}

func (h *harness) importExport(archive []byte, opts ImportOptions) api.Import {
	h.t.Helper()
	result, err := h.engine.Import(context.Background(), bytes.NewReader(archive), opts)
	if err != nil {
		h.t.Fatalf("Import: %v", err)
	}
	return result
}

func imported(t *testing.T, result api.Import, name string) api.ImportedApplication {
	t.Helper()
	for _, a := range result.Applications {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("the import does not mention %s: %+v", name, result.Applications)
	return api.ImportedApplication{}
}

// replica is the one container of the application that is not a job.
func (h *harness) replica(name string) docker.Container {
	h.t.Helper()
	for _, c := range h.rt.Containers() {
		if c.App == name && c.Job == "" {
			return c
		}
	}
	h.t.Fatalf("%s has no container", name)
	return docker.Container{}
}

// seeded is a first server with a database that has data, a web application
// with a domain, a secret, a registry credential and a certificate.
func seeded(t *testing.T) *harness {
	t.Helper()
	h := newHarness(t)
	ctx := context.Background()
	web := app("web", "ghcr.io/acme/web:1.0", 2)
	web.Domain = "web.example.com"
	web.Aliases = []string{"www.example.com"}
	h.deploy(web)
	h.deploy(volumeApp())
	for name, content := range map[string]string{"PG_VERSION": "17", "base/1": "rows"} {
		if err := h.rt.PutFile(h.replica("db").ID, "/var/lib/data/"+name, []byte(content)); err != nil {
			t.Fatal(err)
		}
	}
	if err := h.store.SetSecret(ctx, "DB_PASSWORD", "s3cret-value", time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := h.store.SetRegistry(ctx, "ghcr.io", "acme", "registry-token", time.Now()); err != nil {
		t.Fatal(err)
	}
	pair := certstest.Issue(time.Now().AddDate(0, 6, 0), "web.example.com")
	if _, err := h.engine.SetCertificate(ctx, "web.example.com", pair.Cert, pair.Key); err != nil {
		t.Fatal(err)
	}
	return h
}

func TestAnExportImportedOnAnotherServerBringsApplicationsDataAndSecrets(t *testing.T) {
	a := seeded(t)
	b := newServer(t, otherKey)
	ctx := context.Background()

	result := b.importExport(a.export(), ImportOptions{Source: "upload"})

	if result.Status != api.ImportSucceeded || result.Error != "" {
		t.Fatalf("import = %+v", result)
	}
	if result.Secrets != 1 || result.Registries != 1 || result.Certificates != 1 {
		t.Errorf("secrets, registries, certificates = %d, %d, %d; want 1 each", result.Secrets, result.Registries, result.Certificates)
	}
	// The database has no hostname and goes first.
	if got := []string{result.Applications[0].Name, result.Applications[1].Name}; got[0] != "db" || got[1] != "web" {
		t.Errorf("order = %v, want db before web", got)
	}
	for _, name := range []string{"db", "web"} {
		if got := imported(t, result, name); got.Status != api.ImportAppImported || got.DeploymentID == nil {
			t.Errorf("%s = %+v", name, got)
		}
	}

	db := b.replica("db")
	if !db.Running {
		t.Error("db is not running after the import")
	}
	files := b.rt.Files(db.ID)
	if string(files["/var/lib/data/PG_VERSION"]) != "17" || string(files["/var/lib/data/base/1"]) != "rows" {
		t.Errorf("volume on the new server = %v", files)
	}
	if b.rt.Starts(db.ID) != 1 {
		t.Errorf("db was started %d times, want once: after its volume was filled", b.rt.Starts(db.ID))
	}
	if jobs := b.rt.JobContainers(); len(jobs) != 0 {
		t.Errorf("the container the volume was filled through is still there: %v", jobs)
	}

	d, err := b.store.GetDeployment(ctx, *imported(t, result, "web").DeploymentID)
	if err != nil {
		t.Fatal(err)
	}
	if d.Kind != api.KindImport || d.Status != api.StatusActive || d.Spec.Env["SECRET"] != "hunter2" || d.Spec.Domain != "web.example.com" {
		t.Errorf("web's deployment = kind %s, status %s, env %v", d.Kind, d.Status, d.Spec.Env)
	}

	// Sealed under the importing server's key: its store opens them.
	values, err := b.store.GetSecrets(ctx, []string{"DB_PASSWORD"})
	if err != nil || values["DB_PASSWORD"] != "s3cret-value" {
		t.Errorf("secret = %v, %v", values, err)
	}
	username, password, found, err := b.store.RegistryCredential(ctx, "ghcr.io")
	if err != nil || !found || username != "acme" || password != "registry-token" {
		t.Errorf("registry credential = %q, %q, %v, %v", username, password, found, err)
	}
	certificates, err := b.store.ListCertificates(ctx)
	if err != nil || len(certificates) != 1 || !strings.Contains(certificates[0].KeyPEM, "PRIVATE KEY") {
		t.Errorf("certificates = %d, %v", len(certificates), err)
	}
	// The image came from a registry and was pulled with the credential the
	// export brought.
	var pulledWith *docker.RegistryAuth
	for _, p := range b.rt.Pulls() {
		if p.Image == "ghcr.io/acme/web:1.0" {
			pulledWith = p.Auth
		}
	}
	if pulledWith == nil || pulledWith.Password != "registry-token" {
		t.Errorf("web was pulled with %+v, want the imported credential", pulledWith)
	}
}

func TestAnImportLeavesWhatExistsAloneUnlessToldToOverwrite(t *testing.T) {
	a := seeded(t)
	archive := a.export()
	b := newServer(t, otherKey)
	ctx := context.Background()

	// The new server has a database of the same name with its own data, and
	// a secret of the same name.
	b.deployWithFiles(map[string]string{"PG_VERSION": "16"})
	if err := b.store.SetSecret(ctx, "DB_PASSWORD", "its-own", time.Now()); err != nil {
		t.Fatal(err)
	}
	mine := b.replica("db")

	result := b.importExport(archive, ImportOptions{})
	if got := imported(t, result, "db"); got.Status != api.ImportAppSkipped || !strings.Contains(got.Message, "--overwrite") {
		t.Errorf("db = %+v, want skipped with what to do", got)
	}
	if got := imported(t, result, "web"); got.Status != api.ImportAppImported {
		t.Errorf("web = %+v, want imported: it did not exist", got)
	}
	if c := b.replica("db"); c.ID != mine.ID || string(b.rt.Files(c.ID)["/var/lib/data/PG_VERSION"]) != "16" {
		t.Error("the existing database was touched by an import without --overwrite")
	}
	if values, _ := b.store.GetSecrets(ctx, []string{"DB_PASSWORD"}); values["DB_PASSWORD"] != "its-own" {
		t.Errorf("secret = %q, want the server's own kept", values["DB_PASSWORD"])
	}
	if len(result.Warnings) == 0 || !strings.Contains(strings.Join(result.Warnings, "\n"), "DB_PASSWORD") {
		t.Errorf("warnings = %v, want the kept secret named", result.Warnings)
	}

	result = b.importExport(archive, ImportOptions{Overwrite: true})
	if got := imported(t, result, "db"); got.Status != api.ImportAppImported || len(got.Volumes) != 1 {
		t.Fatalf("db with overwrite = %+v", got)
	}
	files := b.rt.Files(b.replica("db").ID)
	if string(files["/var/lib/data/PG_VERSION"]) != "17" {
		t.Errorf("volume after overwrite = %v, want the export's", files)
	}
	if values, _ := b.store.GetSecrets(ctx, []string{"DB_PASSWORD"}); values["DB_PASSWORD"] != "s3cret-value" {
		t.Errorf("secret after overwrite = %q", values["DB_PASSWORD"])
	}
}

func TestAnImportRefusesAVolumeLeftByADeletedApplication(t *testing.T) {
	a := seeded(t)
	archive := a.export("db")
	b := newServer(t, otherKey)
	b.deployWithFiles(map[string]string{"old": "data"})
	if err := b.engine.Delete(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}

	result := b.importExport(archive, ImportOptions{})
	got := imported(t, result, "db")
	if got.Status != api.ImportAppFailed || !strings.Contains(got.Message, "shipwick volumes rm") {
		t.Fatalf("db = %+v, want a failure that names the volume and what to do", got)
	}
	if !b.rt.VolumeExists("db", "data") {
		t.Error("the leftover volume was removed without --overwrite")
	}
}

func TestAStoppedImportDeploysWithoutStartingAndAPromotionStartsInOrder(t *testing.T) {
	a := seeded(t)
	archive := a.export()
	b := newServer(t, otherKey)
	ctx := context.Background()

	result := b.importExport(archive, ImportOptions{Stopped: true, Overwrite: true})
	if result.Status != api.ImportSucceeded {
		t.Fatalf("import = %+v", result)
	}
	for _, c := range b.rt.Containers() {
		if c.Running || b.rt.Starts(c.ID) != 0 {
			t.Errorf("%s was started by an import that leaves applications stopped", c.Name)
		}
	}
	if len(b.rt.Containers()) != 3 {
		t.Errorf("containers = %d, want db's one and web's two, created", len(b.rt.Containers()))
	}
	app, err := b.store.GetApplication(ctx, "web")
	if err != nil || app.DesiredState != api.DesiredStopped || app.ActiveDeploymentID == nil {
		t.Fatalf("web = %+v, %v", app, err)
	}
	d, _ := b.store.GetDeployment(ctx, *app.ActiveDeploymentID)
	if d.Kind != api.KindStandby || d.Status != api.StatusActive || d.CompletedAt == nil {
		t.Errorf("web's deployment = %s %s", d.Kind, d.Status)
	}
	if string(b.rt.Files(b.replica("db").ID)["/var/lib/data/PG_VERSION"]) != "17" {
		t.Error("the stopped database does not hold the export's data")
	}

	standby, err := b.engine.Standby(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(standby.Applications) != 2 || standby.Applications[0].Name != "db" || standby.Applications[1].Name != "web" {
		t.Fatalf("standby = %+v", standby.Applications)
	}
	wantRecords := []api.DNSRecord{
		{Hostname: "web.example.com", Type: "A", Value: "203.0.113.77"},
		{Hostname: "www.example.com", Type: "A", Value: "203.0.113.77"},
	}
	if len(standby.Records) != 2 || standby.Records[0] != wantRecords[0] || standby.Records[1] != wantRecords[1] {
		t.Errorf("records = %+v, want %+v", standby.Records, wantRecords)
	}

	// The export is imported again, as a schedule would: the stopped
	// applications are replaced, still stopped.
	before := b.replica("db").ID
	result = b.importExport(archive, ImportOptions{Stopped: true, Overwrite: true})
	if result.Status != api.ImportSucceeded || b.replica("db").ID == before || b.replica("db").Running {
		t.Errorf("second stopped import = %s, db replaced: %v", result.Status, b.replica("db").ID != before)
	}

	promotion, err := b.engine.Promote(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if len(promotion.Applications) != 2 || promotion.Applications[0].Name != "db" ||
		promotion.Applications[0].Status != api.PromotedRunning || promotion.Applications[1].Status != api.PromotedRunning {
		t.Fatalf("promotion = %+v", promotion.Applications)
	}
	if len(promotion.Records) != 2 {
		t.Errorf("promotion records = %+v", promotion.Records)
	}
	for _, c := range b.rt.Containers() {
		if !c.Running {
			t.Errorf("%s is not running after the promotion", c.Name)
		}
	}
	if app, _ := b.store.GetApplication(ctx, "db"); app.DesiredState != api.DesiredRunning {
		t.Errorf("db desired state = %s after the promotion", app.DesiredState)
	}
	if again, _ := b.engine.Promote(ctx); len(again.Applications) != 0 {
		t.Errorf("a second promotion started %+v", again.Applications)
	}

	// The old server's next export arrives after the promotion. What runs
	// here now is the service; it is not stopped and its data not replaced.
	running := b.replica("db").ID
	result = b.importExport(archive, ImportOptions{Stopped: true, Overwrite: true})
	for _, name := range []string{"db", "web"} {
		if got := imported(t, result, name); got.Status != api.ImportAppSkipped || !strings.Contains(got.Message, "running") {
			t.Errorf("%s after promotion = %+v, want skipped", name, got)
		}
	}
	if c := b.replica("db"); c.ID != running || !c.Running {
		t.Error("a standby import touched an application that runs")
	}
	// Nor does it put the old server's secrets over what the promoted one has.
	if err := b.store.SetSecret(ctx, "DB_PASSWORD", "changed-after-promotion", time.Now()); err != nil {
		t.Fatal(err)
	}
	result = b.importExport(archive, ImportOptions{Stopped: true, Overwrite: true})
	if values, _ := b.store.GetSecrets(ctx, []string{"DB_PASSWORD"}); values["DB_PASSWORD"] != "changed-after-promotion" || result.Secrets != 0 {
		t.Errorf("secret after an import into a promoted server = %q, %d stored", values["DB_PASSWORD"], result.Secrets)
	}
}

func TestAnApplicationStoppedWhereItComesFromIsImportedStopped(t *testing.T) {
	a := seeded(t)
	if err := a.engine.Stop(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}
	b := newServer(t, otherKey)
	result := b.importExport(a.export("db"), ImportOptions{})
	if got := imported(t, result, "db"); got.Status != api.ImportAppImported {
		t.Fatalf("db = %+v", got)
	}
	c := b.replica("db")
	if c.Running || b.rt.Starts(c.ID) != 0 {
		t.Error("an application that was stopped on the old server was started on the new one")
	}
	// Not a standby's: a promotion is not what starts it.
	if standby, _ := b.engine.Standby(context.Background()); len(standby.Applications) != 0 {
		t.Errorf("standby lists %+v", standby.Applications)
	}
	if err := b.engine.Start(context.Background(), "db"); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if !b.replica("db").Running {
		t.Error("shipwick start did not start it")
	}
}

func TestAnImageBuiltByDeployTravelsInTheExport(t *testing.T) {
	a := newHarness(t)
	local := app("api", "shipwick.local/api:20260301-ab12", 1)
	local.Build = &spec.Build{}
	a.rt.AddLocalImage(local.Image)
	a.deploy(local)

	b := newServer(t, otherKey)
	result := b.importExport(a.export(), ImportOptions{})
	if got := imported(t, result, "api"); got.Status != api.ImportAppImported {
		t.Fatalf("api = %+v", got)
	}
	if loaded := b.rt.LoadedImages(); len(loaded) != 1 || loaded[0] != local.Image {
		t.Errorf("loaded images = %v", loaded)
	}
	if !b.replica("api").Running {
		t.Error("api is not running")
	}

	// A daemon that cannot write the image out: the export says so, and the
	// import says what to do instead of deploying something that cannot start.
	a.rt.SaveErr = errors.New("content digest sha256:0f not found")
	c := newServer(t, otherKey)
	result = c.importExport(a.export(), ImportOptions{})
	got := imported(t, result, "api")
	if got.Status != api.ImportAppSkipped || !strings.Contains(got.Message, "shipwick deploy") || !strings.Contains(got.Message, "content digest") {
		t.Errorf("api without its image = %+v", got)
	}
	if len(c.rt.Containers()) != 0 {
		t.Error("an application without its image was created")
	}
}

func TestAStaticApplicationTravelsWithItsFolder(t *testing.T) {
	a, _ := newStaticHarness(t)
	a.deployStatic(site("docs"), map[string]string{"index.html": "<h1>docs</h1>", "css/site.css": "body{}"})
	archive := a.export()

	b, _ := newStaticHarness(t)
	result := b.importExport(archive, ImportOptions{})
	if got := imported(t, result, "docs"); got.Status != api.ImportAppImported {
		t.Fatalf("docs = %+v", got)
	}
	served := b.served("docs")
	if len(served) != 2 || !strings.HasSuffix(served[1], "/index.html") {
		t.Errorf("served on the new server = %v", served)
	}
}

func TestWhatIsNotAnExportIsRefusedInWords(t *testing.T) {
	b := newServer(t, otherKey)
	_, err := b.engine.Import(context.Background(), bytes.NewReader(tarOf(t, map[string]string{"index.html": "x"})), ImportOptions{})
	var invalid *InvalidExportError
	if !errors.As(err, &invalid) || !strings.Contains(invalid.Reason, "not an export") {
		t.Fatalf("a tar that is not an export: %v", err)
	}

	a := seeded(t)
	archive := a.export()
	result, err := b.engine.Import(context.Background(), bytes.NewReader(archive[:len(archive)/2]), ImportOptions{})
	if !errors.As(err, &invalid) || !strings.Contains(invalid.Reason, "damaged or incomplete") {
		t.Fatalf("half an export: %v", err)
	}
	if result.Status != api.ImportFailed || result.CompletedAt == nil {
		t.Errorf("record of a failed import = %+v", result)
	}
	if status, ok := b.engine.ImportStatus(); !ok || status.Status != api.ImportFailed {
		t.Errorf("ImportStatus = %+v, %v", status, ok)
	}
}

func TestAnExportNeverHoldsASealedValue(t *testing.T) {
	a := seeded(t)
	archive := a.export()
	// What the store keeps is "enc1:…"; an export that carried it would be
	// useless on a server with another key.
	if bytes.Contains(archive, []byte("enc1:")) {
		t.Error("the export holds a value as the database seals it")
	}
	if !bytes.Contains(archive, []byte("s3cret-value")) || !bytes.Contains(archive, []byte("hunter2")) {
		t.Error("the export does not hold the values in clear, for its encryption to seal")
	}
}

func TestExportingAnApplicationThatIsBeingDeployedFailsAndNamesIt(t *testing.T) {
	a := seeded(t)
	if wait, err := a.engine.tryLock("db", false); wait != nil || err != nil {
		t.Fatal("could not take the lock")
	}
	defer a.engine.unlock("db")
	err := a.engine.Export(context.Background(), io.Discard, nil)
	if !errors.Is(err, ErrBusy) || !strings.HasPrefix(err.Error(), "db: ") {
		t.Fatalf("Export = %v, want ErrBusy naming db", err)
	}
	if err := a.engine.Export(context.Background(), io.Discard, []string{"nope"}); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("Export of an unknown application = %v", err)
	}
}

func TestAMemberLargerThanAPartReadsBackWhole(t *testing.T) {
	big := make([]byte, 2*exportPartSize+12345)
	if _, err := rand.Read(big); err != nil {
		t.Fatal(err)
	}
	var buf bytes.Buffer
	xw := newExportWriter(&buf, time.Now())
	if _, err := xw.stream("applications/db/volumes/data.tar", bytes.NewReader(big)); err != nil {
		t.Fatal(err)
	}
	if _, err := xw.stream("applications/db/volumes/empty.tar", strings.NewReader("")); err != nil {
		t.Fatal(err)
	}
	if err := xw.json("applications/web/app.json", exportApp{Name: "web"}); err != nil {
		t.Fatal(err)
	}
	xw.close()

	xr := newExportReader(&buf)
	member, err := xr.stream("applications/db/volumes/data.tar")
	if err != nil {
		t.Fatal(err)
	}
	got, err := io.ReadAll(member)
	if err != nil || !bytes.Equal(got, big) {
		t.Fatalf("read back %d bytes, %v; want %d", len(got), err, len(big))
	}
	if _, err := xr.stream("applications/db/volumes/other.tar"); err == nil {
		t.Error("a member that is not next was handed out")
	}
	empty, err := xr.stream("applications/db/volumes/empty.tar")
	if err != nil {
		t.Fatal(err)
	}
	if got, _ := io.ReadAll(empty); len(got) != 0 {
		t.Errorf("empty member read %d bytes", len(got))
	}
	var next exportApp
	if err := xr.json("applications/web/app.json", &next); err != nil || next.Name != "web" {
		t.Errorf("the entry after the members = %+v, %v", next, err)
	}
}

func TestAScheduledExportGoesToTheBucketAndAStandbyImportsTheNewest(t *testing.T) {
	a := seeded(t)
	bucket, objects := fakeBucket(t)
	withBackups(a, testPassphrase, bucket)
	schedule, err := cron.Parse("0 4 * * *")
	if err != nil {
		t.Fatal(err)
	}
	a.engine.opts.Transfer = TransferOptions{ExportSchedule: &schedule, ExportsKept: 2}
	ctx := context.Background()

	a.engine.scheduleTransfers(ctx, at(3, 58, 0))
	a.engine.scheduleTransfers(ctx, at(3, 59, 30))
	a.engine.Wait()
	if runs, _ := a.engine.Exports(ctx, 0); len(runs) != 0 {
		t.Fatalf("exports before the schedule fires = %+v", runs)
	}
	a.engine.scheduleTransfers(ctx, at(4, 0, 1))
	a.engine.scheduleTransfers(ctx, at(4, 0, 2))
	a.engine.Wait()
	runs, err := a.engine.Exports(ctx, 0)
	if err != nil || len(runs) != 1 || runs[0].Status != api.BackupSucceeded || !runs[0].Encrypted || runs[0].Trigger != api.BackupTriggerSchedule {
		t.Fatalf("exports after 04:00 = %+v, %v", runs, err)
	}
	var key string
	for _, k := range objects.Keys() {
		if strings.HasPrefix(k, ExportOwner+"/") {
			key = k
		}
	}
	if !strings.HasSuffix(key, "/export.tar.enc") {
		t.Fatalf("bucket holds %v, want the encrypted export", objects.Keys())
	}
	if bytes.Contains(objects.Objects()[key], []byte("hunter2")) {
		t.Error("the export in the bucket holds a secret in clear")
	}

	// The standby reads the same bucket with the same passphrase.
	b := newServer(t, otherKey)
	if _, err := b.engine.StartStandbyPull(ctx); !errors.Is(err, ErrNoStandbySource) {
		t.Fatalf("a pull without a bucket = %v", err)
	}
	every, _ := cron.Parse("*/15 * * * *")
	b.engine.opts.Transfer = TransferOptions{StandbySchedule: &every, StandbySource: backup.New(t.TempDir(), bucket, testPassphrase)}
	b.engine.scheduleTransfers(ctx, at(4, 15, 0))
	b.engine.Wait()

	status, ok := b.engine.ImportStatus()
	if !ok || status.Status != api.ImportSucceeded || !status.Stopped || len(status.Applications) != 2 {
		t.Fatalf("the standby's import = %+v, %v", status, ok)
	}
	if c := b.replica("db"); c.Running || string(b.rt.Files(c.ID)["/var/lib/data/PG_VERSION"]) != "17" {
		t.Error("the standby's database is running, or does not hold the data")
	}
	standby, _ := b.engine.Standby(ctx)
	if standby.Pull == nil || standby.Pull.LastExport != runs[0].ID || standby.Pull.LastError != "" || standby.Pull.Schedule != "*/15 * * * *" {
		t.Errorf("pull = %+v", standby.Pull)
	}

	// The same export is not imported a second time by the schedule.
	before := b.replica("db").ID
	b.engine.scheduleTransfers(ctx, at(4, 30, 0))
	b.engine.Wait()
	if b.replica("db").ID != before {
		t.Error("the schedule imported an export it had imported already")
	}
}

func TestAnExportToTheBackupDestinationNeedsAPassphrase(t *testing.T) {
	a := seeded(t)
	if _, err := a.engine.StartExport(context.Background(), api.BackupTriggerManual); !errors.Is(err, ErrBackupsDisabled) {
		t.Fatalf("without a destination: %v", err)
	}
	withBackups(a, "", nil)
	if _, err := a.engine.StartExport(context.Background(), api.BackupTriggerManual); !errors.Is(err, ErrExportNotEncrypted) {
		t.Fatalf("without a passphrase: %v", err)
	}
}

func TestAnImportedConfigurationIsValidatedAgain(t *testing.T) {
	good := exportApp{Name: "db", Spec: volumeApp(), Volumes: []string{"data"}}
	if _, err := checkImported("db", good); err != nil {
		t.Fatalf("a valid entry: %v", err)
	}
	for name, change := range map[string]func(e *exportApp){
		"another name":       func(e *exportApp) { e.Spec.Name = "web" },
		"a name with a path": func(e *exportApp) { e.Name, e.Spec.Name = "../db", "../db" },
		"a bad domain":       func(e *exportApp) { e.Spec.Domain = "exa mple.com" },
		"a volume path":      func(e *exportApp) { e.Spec.Volumes[0].Path = "data" },
		"a volume name":      func(e *exportApp) { e.Spec.Volumes[0].Name = "../x" },
		"a missing archive":  func(e *exportApp) { e.Volumes = nil },
		"two replicas":       func(e *exportApp) { e.Spec.Replicas = 2 },
		// What the import took as the export had it, before it held the
		// configuration to the rules of a deploy.yaml.
		"a health path": func(e *exportApp) {
			e.Spec.Health = &spec.Health{Path: "health check", Interval: spec.Duration(time.Second), Timeout: spec.Duration(time.Second), Retries: 1}
		},
		"a proxy header": func(e *exportApp) {
			e.Spec.Domain, e.Spec.Proxy = "db.example.com", &spec.Proxy{Headers: map[string]string{"Connection": "close"}}
		},
		"a published port": func(e *exportApp) { e.Spec.Publish = []spec.Publish{{Port: 5432, Host: 80, Protocol: "tcp"}} },
		"a logging option": func(e *exportApp) {
			e.Spec.Logging = &spec.Logging{Driver: "syslog", Options: map[string]string{"syslog-address": "unix:///dev/log"}}
		},
	} {
		entry := good
		entry.Spec = volumeApp()
		entry.Volumes = []string{"data"}
		change(&entry)
		filed := "db"
		if name == "a name with a path" {
			filed = entry.Name
		}
		if _, err := checkImported(filed, entry); err == nil {
			t.Errorf("%s was accepted", name)
		}
	}
}

func TestDueFiresOncePerScheduledMinute(t *testing.T) {
	s, _ := cron.Parse("*/10 * * * *")
	var last time.Time
	if due(&s, &last, at(4, 9, 0)) {
		t.Error("fired at 04:09")
	}
	if !due(&s, &last, at(4, 10, 0)) {
		t.Error("did not fire at 04:10")
	}
	if due(&s, &last, at(4, 10, 0)) {
		t.Error("fired twice in the same minute")
	}
	// Minutes the loop did not see — the agent was busy — still count.
	if !due(&s, &last, at(4, 25, 0)) {
		t.Error("did not fire for 04:20, which passed unseen")
	}
	if due(nil, &last, at(5, 0, 0)) {
		t.Error("no schedule fired")
	}
}
