package deploy

import (
	"bytes"
	"context"
	"errors"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/cron"
)

// gated is a runtime in which starting a container waits until the test lets
// it go on: how a test looks at a promotion that is under way.
type gated struct {
	Runtime
	once    sync.Once
	reached chan struct{}
	release chan struct{}
}

func gateStarts(h *harness) *gated {
	g := &gated{Runtime: h.rt, reached: make(chan struct{}), release: make(chan struct{})}
	h.engine.rt = g
	return g
}

func (g *gated) StartContainer(ctx context.Context, id string) error {
	g.once.Do(func() { close(g.reached) })
	select {
	case <-g.release:
	case <-ctx.Done():
		return ctx.Err()
	}
	return g.Runtime.StartContainer(ctx, id)
}

// standbyOf is a second server that holds the first one's applications,
// imported stopped.
func standbyOf(t *testing.T, a *harness) *harness {
	t.Helper()
	b := newServer(t, otherKey)
	if result := b.importExport(a.export(), ImportOptions{Stopped: true, Overwrite: true}); result.Status != api.ImportSucceeded {
		t.Fatalf("import = %+v", result)
	}
	return b
}

func promoted(t *testing.T, p api.Promotion, name string) api.PromotedApplication {
	t.Helper()
	for _, a := range p.Applications {
		if a.Name == name {
			return a
		}
	}
	t.Fatalf("the promotion does not mention %s: %+v", name, p.Applications)
	return api.PromotedApplication{}
}

func TestAPromotionAnswersAtOnceAndItsRecordFollowsEachApplication(t *testing.T) {
	b := standbyOf(t, seeded(t))
	ctx := context.Background()
	if _, ok := b.engine.Promotion(); ok {
		t.Fatal("a server that was never promoted has a promotion")
	}

	gate := gateStarts(b)
	begun, err := b.engine.StartPromotion(WithActor(ctx, "ops"))
	if err != nil {
		t.Fatal(err)
	}
	if begun.ID != 1 || begun.Status != api.PromotionRunning || begun.CompletedAt != nil || len(begun.Records) != 2 ||
		len(begun.Applications) != 2 || begun.Applications[0] != (api.PromotedApplication{Name: "db", Status: api.PromotedPending}) ||
		begun.Applications[1].Name != "web" {
		t.Fatalf("the promotion as it begins = %+v", begun)
	}

	<-gate.reached
	now, _ := b.engine.Promotion()
	if promoted(t, now, "db").Status != api.PromotedStarting || promoted(t, now, "web").Status != api.PromotedPending || now.CompletedAt != nil {
		t.Errorf("while db is being started: %+v", now.Applications)
	}
	if _, err := b.engine.StartPromotion(ctx); !errors.Is(err, ErrPromotionInProgress) {
		t.Errorf("a second promotion while one runs: %v", err)
	}
	if _, err := b.engine.Import(ctx, bytes.NewReader(nil), ImportOptions{Stopped: true, Overwrite: true}); !errors.Is(err, ErrPromotionInProgress) {
		t.Errorf("an import while a promotion runs: %v", err)
	}
	if standby, _ := b.engine.Standby(ctx); standby.Promotion == nil || standby.Promotion.ID != 1 {
		t.Errorf("GET /standby does not carry the promotion: %+v", standby.Promotion)
	}

	close(gate.release)
	final, err := b.engine.AwaitPromotion(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if final.Status != api.PromotionSucceeded || final.CompletedAt == nil || len(final.Records) != 2 ||
		promoted(t, final, "db").Status != api.PromotedRunning || promoted(t, final, "web").Status != api.PromotedRunning {
		t.Fatalf("the promotion when it has ended = %+v", final)
	}
	for _, c := range b.rt.Containers() {
		if !c.Running {
			t.Errorf("%s is not running after the promotion", c.Name)
		}
	}
	// Who asked for it is who started the applications.
	events, _ := b.engine.Events(ctx, "db", 10)
	said := false
	for _, e := range events {
		said = said || e.Message == "Application started by ops"
	}
	if !said {
		t.Errorf("db's events do not say who started it: %+v", events)
	}

	// Nothing waits any more: no promotion begins, and the last one stays.
	again, err := b.engine.StartPromotion(ctx)
	if err != nil || again.CompletedAt == nil || again.ID != 0 || len(again.Applications) != 0 {
		t.Errorf("a promotion with nothing to start = %+v, %v", again, err)
	}
	if last, _ := b.engine.Promotion(); last.ID != 1 || len(last.Applications) != 2 {
		t.Errorf("the record of the promotion was replaced by one that started nothing: %+v", last)
	}
}

func TestAnApplicationThatCannotBeStartedFailsThePromotionAndNotTheOthers(t *testing.T) {
	b := standbyOf(t, seeded(t))
	ctx := context.Background()
	// db's container is gone; web's are there.
	if err := b.rt.RemoveContainer(ctx, b.replica("db").ID); err != nil {
		t.Fatal(err)
	}
	final, err := b.engine.Promote(ctx)
	if err != nil {
		t.Fatal(err)
	}
	db := promoted(t, final, "db")
	if final.Status != api.PromotionFailed || db.Status != api.PromotedFailed || !strings.Contains(db.Message, "deploy the application again") {
		t.Errorf("promotion = %s, db = %+v", final.Status, db)
	}
	if promoted(t, final, "web").Status != api.PromotedRunning {
		t.Errorf("web = %+v: one application that fails does not stop the promotion", promoted(t, final, "web"))
	}
}

func TestAPromotionIsRefusedWhileAnImportRuns(t *testing.T) {
	a := seeded(t)
	b := standbyOf(t, a)
	ctx := context.Background()

	// The upload's context, which ends with the request.
	uploading, hangUp := context.WithCancel(ctx)
	stall := stallAt(b, "pull")
	done := make(chan struct{})
	go func() {
		defer close(done)
		b.engine.Import(uploading, bytes.NewReader(a.export()), ImportOptions{Stopped: true, Overwrite: true})
	}()
	<-stall.reached
	if _, err := b.engine.StartPromotion(ctx); !errors.Is(err, ErrImportInProgress) {
		t.Errorf("a promotion while an import runs: %v", err)
	}
	hangUp()
	<-done
}

func TestAPromotionInterruptedByARestartGoesOnWhereItWas(t *testing.T) {
	b := standbyOf(t, seeded(t))
	ctx := context.Background()

	stall := stallAt(b, "start")
	if _, err := b.engine.StartPromotion(WithActor(ctx, "ops")); err != nil {
		t.Fatal(err)
	}
	<-stall.reached
	b.restart()

	final, ok := b.engine.Promotion()
	if !ok || final.ID != 1 || final.Status != api.PromotionSucceeded || final.CompletedAt == nil || len(final.Records) != 2 {
		t.Fatalf("the promotion after the restart = %+v, %v", final, ok)
	}
	for _, name := range []string{"db", "web"} {
		if got := promoted(t, final, name); got.Status != api.PromotedRunning {
			t.Errorf("%s = %+v", name, got)
		}
	}
	for _, c := range b.rt.Containers() {
		if !c.Running || b.rt.Starts(c.ID) != 1 {
			t.Errorf("%s: running %v, started %d times; want running, started once", c.Name, c.Running, b.rt.Starts(c.ID))
		}
	}
	if standby, _ := b.engine.Standby(ctx); len(standby.Applications) != 0 {
		t.Errorf("still waiting for a promotion: %+v", standby.Applications)
	}
	events, _ := b.engine.Events(ctx, "web", 10)
	said := false
	for _, e := range events {
		said = said || e.Message == "Application started by ops"
	}
	if !said {
		t.Errorf("web was started after the restart, and its events do not say for whom: %+v", events)
	}
}

func TestAResumedPromotionDoesNotStartAgainWhatItHadStarted(t *testing.T) {
	b := standbyOf(t, seeded(t))
	ctx := context.Background()

	// What an agent leaves that died while it waited for db to be ready: db
	// started and recorded as running, the promotion at db, web not reached.
	if err := b.engine.Start(ctx, "db"); err != nil {
		t.Fatal(err)
	}
	left := promotionRecord{Promotion: api.Promotion{
		ID: 4, Status: api.PromotionRunning, StartedAt: time.Now().UTC(), Records: []api.DNSRecord{{Hostname: "web.example.com", Type: "A", Value: "203.0.113.77"}},
		Applications: []api.PromotedApplication{{Name: "db", Status: api.PromotedStarting}, {Name: "web", Status: api.PromotedPending}},
	}}
	if err := b.store.SetTransferState(ctx, store.TransferPromotion, left, time.Now()); err != nil {
		t.Fatal(err)
	}
	b.restart()

	final, _ := b.engine.Promotion()
	if final.ID != 4 || final.Status != api.PromotionSucceeded || promoted(t, final, "db").Status != api.PromotedRunning || promoted(t, final, "web").Status != api.PromotedRunning {
		t.Fatalf("the promotion after the restart = %+v", final)
	}
	if n := b.rt.Starts(b.replica("db").ID); n != 1 {
		t.Errorf("db was started %d times, want once: it was running when the agent came back", n)
	}
	if !b.replica("web").Running {
		t.Error("web, which the promotion had not reached, was not started after the restart")
	}
}

func TestAPromotionThatHadEndedIsStillKnownAfterARestart(t *testing.T) {
	b := standbyOf(t, seeded(t))
	before, err := b.engine.Promote(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	b.restart()
	after, ok := b.engine.Promotion()
	if !ok || after.ID != before.ID || after.Status != api.PromotionSucceeded || !after.CompletedAt.Equal(*before.CompletedAt) || len(after.Applications) != 2 {
		t.Errorf("the promotion after a restart = %+v, %v; want %+v", after, ok, before)
	}
	for _, c := range b.rt.Containers() {
		if b.rt.Starts(c.ID) != 1 {
			t.Errorf("%s was started again by an agent that found a finished promotion", c.Name)
		}
	}
}

func TestTheLastImportAndTheLastPullSurviveARestart(t *testing.T) {
	a := seeded(t)
	bucket, _ := fakeBucket(t)
	withBackups(a, testPassphrase, bucket)
	ctx := context.Background()
	if _, err := a.engine.StartExport(ctx, api.BackupTriggerManual); err != nil {
		t.Fatal(err)
	}
	a.engine.Wait()

	b := newServer(t, otherKey)
	every, _ := cron.Parse("*/15 * * * *")
	b.engine.opts.Transfer = TransferOptions{StandbySchedule: &every, StandbySource: backup.New(t.TempDir(), bucket, testPassphrase)}
	b.engine.scheduleTransfers(ctx, at(4, 15, 0))
	b.engine.Wait()
	before, ok := b.engine.ImportStatus()
	if !ok || before.Status != api.ImportSucceeded {
		t.Fatalf("the standby's import = %+v, %v", before, ok)
	}
	pulled, _ := b.engine.Standby(ctx)
	db := b.replica("db").ID

	b.restart()

	after, ok := b.engine.ImportStatus()
	if !ok || after.Status != api.ImportSucceeded || after.Source != before.Source || !after.CompletedAt.Equal(*before.CompletedAt) ||
		len(after.Applications) != 2 || after.Applications[0].DeploymentID == nil || after.Secrets != before.Secrets {
		t.Errorf("the last import after a restart = %+v, %v; want %+v", after, ok, before)
	}
	standby, _ := b.engine.Standby(ctx)
	if standby.Pull == nil || standby.Pull.LastExport == 0 || standby.Pull.LastExport != pulled.Pull.LastExport ||
		standby.Pull.LastAt == nil || !standby.Pull.LastAt.Equal(*pulled.Pull.LastAt) || standby.Pull.LastError != "" {
		t.Errorf("pull after a restart = %+v, want %+v", standby.Pull, pulled.Pull)
	}
	// The export it imported before the restart is not imported once more.
	b.engine.scheduleTransfers(ctx, at(4, 30, 0))
	b.engine.Wait()
	if b.replica("db").ID != db {
		t.Error("after a restart the schedule imported the export it had imported already")
	}
}

func TestAPullThatFailedIsRememberedAsFailedAndTriedAgain(t *testing.T) {
	bucket, _ := fakeBucket(t)
	b := newServer(t, otherKey)
	every, _ := cron.Parse("*/15 * * * *")
	b.engine.opts.Transfer = TransferOptions{StandbySchedule: &every, StandbySource: backup.New(t.TempDir(), bucket, testPassphrase)}
	ctx := context.Background()
	b.engine.scheduleTransfers(ctx, at(4, 15, 0))
	b.engine.Wait()
	b.restart()
	standby, _ := b.engine.Standby(ctx)
	if standby.Pull == nil || standby.Pull.LastError == "" || standby.Pull.LastAt == nil || !strings.Contains(standby.Pull.LastError, "no export") {
		t.Errorf("pull after a restart = %+v, want the failure of the last attempt", standby.Pull)
	}
}

func TestAnImportTheAgentDiedUnderIsFoundFailed(t *testing.T) {
	b := newServer(t, otherKey)
	ctx := context.Background()
	id := int64(3)
	left := api.Import{Status: api.ImportRunning, Source: "upload", StartedAt: time.Now().UTC(), Warnings: []string{}, Applications: []api.ImportedApplication{
		{Name: "db", Status: api.ImportAppImported, DeploymentID: &id, Volumes: []string{"data"}},
		{Name: "web", Status: api.ImportAppRunning, Volumes: []string{}},
		{Name: "worker", Status: api.ImportAppPending, Volumes: []string{}},
	}}
	if err := b.store.SetTransferState(ctx, store.TransferImport, left, time.Now()); err != nil {
		t.Fatal(err)
	}
	b.restart()

	got, ok := b.engine.ImportStatus()
	if !ok || got.Status != api.ImportFailed || got.CompletedAt == nil || !strings.Contains(got.Error, "restarted") || !strings.Contains(got.Error, "--overwrite") {
		t.Fatalf("the import after the restart = %+v, %v", got, ok)
	}
	if imported(t, got, "db").Status != api.ImportAppImported {
		t.Errorf("db = %+v: what was imported is imported", imported(t, got, "db"))
	}
	for _, name := range []string{"web", "worker"} {
		if a := imported(t, got, name); a.Status != api.ImportAppFailed || a.Message == "" {
			t.Errorf("%s = %+v, want failed with a reason", name, a)
		}
	}
	// And the next import is not refused as a second one.
	if _, err := b.engine.Import(ctx, bytes.NewReader(seeded(t).export("db")), ImportOptions{}); err != nil {
		t.Errorf("an import after the restart: %v", err)
	}
}

// interruptedImport imports archive into b, stops the agent when the runtime
// reaches the given operation, starts it again and returns the deployment the
// import had begun for the application.
func interruptedImport(t *testing.T, b *harness, archive []byte, opts ImportOptions, at, name string) store.Deployment {
	t.Helper()
	// The upload's context: the API ends it when the agent is told to stop.
	uploading, hangUp := context.WithCancel(context.Background())
	stall := stallAt(b, at)
	done := make(chan api.Import, 1)
	go func() {
		result, _ := b.engine.Import(uploading, bytes.NewReader(archive), opts)
		done <- result
	}()
	<-stall.reached
	hangUp()
	result := <-done
	b.restart()
	// The import ends with its upload and says which deployment it left.
	if got := imported(t, result, name); result.Status != api.ImportFailed || got.DeploymentID == nil || !strings.Contains(got.Message, "shipwick status "+name) {
		t.Fatalf("the import the agent stopped under = %+v", result)
	}
	if status, ok := b.engine.ImportStatus(); !ok || status.Status != api.ImportFailed || status.CompletedAt == nil {
		t.Errorf("the import after the restart = %+v, %v", status, ok)
	}
	latest, err := b.store.ListDeployments(context.Background(), store.DeploymentFilter{Application: name, Limit: 1})
	if err != nil || len(latest) != 1 {
		t.Fatalf("deployments of %s: %+v, %v", name, latest, err)
	}
	return latest[0]
}

func (h *harness) wantDormant(d store.Deployment) {
	h.t.Helper()
	ctx := context.Background()
	if d.Status != api.StatusActive || d.CompletedAt == nil {
		h.t.Fatalf("the resumed deployment: %s (%s), completed %v\n%s", d.Status, d.Error, d.CompletedAt, h.steps(d.ID))
	}
	if !strings.Contains(h.steps(d.ID), "Resumed after the agent restarted") || !strings.Contains(h.steps(d.ID), "not started") {
		h.t.Errorf("the events do not say that it resumed and started nothing:\n%s", h.steps(d.ID))
	}
	replicas := 0
	for _, c := range h.rt.Containers() {
		if c.App != d.Application {
			continue
		}
		replicas++
		if c.Running || h.rt.Starts(c.ID) != 0 || c.DeploymentID != d.ID {
			h.t.Errorf("%s of deployment %d: running %v, started %d times; want created and never started", c.Name, c.DeploymentID, c.Running, h.rt.Starts(c.ID))
		}
	}
	if replicas != d.Spec.Replicas {
		h.t.Errorf("%s has %d containers, want %d", d.Application, replicas, d.Spec.Replicas)
	}
	if app, err := h.store.GetApplication(ctx, d.Application); err != nil || app.DesiredState != api.DesiredStopped {
		h.t.Errorf("%s is recorded as %s, want stopped (%v)", d.Application, app.DesiredState, err)
	}
}

func TestAnImportOfAStoppedApplicationInterruptedByARestartStaysStopped(t *testing.T) {
	// web is cut off while its image is pulled and before its replicas exist;
	// db, whose image is pulled once more before its volume is filled, after
	// the volume holds the export's data.
	for _, tc := range [][2]string{{"web", "pull"}, {"web", "create"}, {"db", "create"}} {
		name, at := tc[0], tc[1]
		t.Run(name+" at "+at, func(t *testing.T) {
			a := seeded(t)
			if err := a.engine.Stop(context.Background(), name); err != nil {
				t.Fatal(err)
			}
			b := newServer(t, otherKey)
			// Not an import that leaves everything stopped: only this
			// application was stopped where it comes from.
			d := interruptedImport(t, b, a.export(name), ImportOptions{}, at, name)
			if d.Kind != api.KindImport {
				t.Fatalf("kind = %s", d.Kind)
			}
			b.wantDormant(d)
			if name == "db" && string(b.rt.Files(b.replica("db").ID)["/var/lib/data/PG_VERSION"]) != "17" {
				t.Error("the stopped database does not hold the export's data")
			}
			if err := b.engine.Start(context.Background(), name); err != nil || !b.replica(name).Running {
				t.Errorf("shipwick start after the resumed import: %v", err)
			}
		})
	}
}

func TestAStandbyImportInterruptedByARestartStaysStoppedAndIsServedOncePromoted(t *testing.T) {
	for _, at := range []string{"pull", "create"} {
		t.Run(at, func(t *testing.T) {
			a := seeded(t)
			s, p := newRouted(t)
			b := s.harness
			d := interruptedImport(t, b, a.export("web"), ImportOptions{Stopped: true, Overwrite: true}, at, "web")
			if d.Kind != api.KindStandby {
				t.Fatalf("kind = %s", d.Kind)
			}
			b.wantDormant(d)
			if got := p.upstreams("web.example.com"); len(got) != 0 {
				t.Errorf("a stopped application is served by %v", got)
			}

			final, err := b.engine.Promote(context.Background())
			if err != nil || promoted(t, final, "web").Status != api.PromotedRunning {
				t.Fatalf("promotion = %+v, %v", final, err)
			}
			// The resumed deployment had taken over the application's routing;
			// had it kept it, the promoted application would be served by nobody.
			if got := p.upstreams("web.example.com"); len(got) != 2 {
				t.Errorf("after the promotion web.example.com is served by %v, want its two replicas", got)
			}
		})
	}
}

func TestADormantDeploymentIsRecordedAsOneBeforeItBegins(t *testing.T) {
	a := seeded(t)
	if err := a.engine.Stop(context.Background(), "db"); err != nil {
		t.Fatal(err)
	}
	b := newServer(t, otherKey)
	result := b.importExport(a.export(), ImportOptions{})
	ctx := context.Background()
	for name, want := range map[string]bool{"db": true, "web": false} {
		dormant, err := b.store.DeploymentDormant(ctx, *imported(t, result, name).DeploymentID)
		if err != nil || dormant != want {
			t.Errorf("%s recorded as dormant: %v, %v; want %v", name, dormant, err, want)
		}
	}
}
