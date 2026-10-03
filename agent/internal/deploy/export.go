package deploy

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"sort"
	"time"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/cron"
	"github.com/shipwick/shipwick/pkg/spec"
	"github.com/shipwick/shipwick/pkg/version"
)

// An export is everything a server would need to be built again somewhere
// else: every application's active configuration with its values in clear,
// the stored secrets, registry credentials and certificates, the images that
// exist nowhere but here, the folders of static applications and an archive
// of every volume (see exportfile.go for the format). What is sealed in the
// database is opened here and sealed again by the server that imports it,
// under its own key: the two keys never meet.
//
// The engine writes the archive in clear to whatever it is handed; the two
// callers hand it a writer that encrypts — the API with the passphrase of the
// request, the scheduled export through backup.Storage with the agent's — and
// nothing else may call it.

const (
	// ExportOwner owns the scheduled exports among the backups, the way
	// backup.StateOwner owns the agent's state. Not an application's name:
	// those hold no underscore.
	ExportOwner = "_export"
	// exportFile is the one file of a scheduled export.
	exportFile = "export.tar"
	// defaultExportsKept is how many scheduled exports are kept.
	defaultExportsKept = 3
)

var (
	// ErrExportInProgress means an export to the backup destination is being
	// written.
	ErrExportInProgress = errors.New("an export is being written already; see it with: shipwick export --list")
	// ErrExportNotEncrypted means the agent has no passphrase to write an
	// export to its backup destination with.
	ErrExportNotEncrypted = errors.New("an export holds every secret of the server and is only ever written encrypted: SHIPWICK_BACKUP_PASSPHRASE is not set on the agent")
)

// TransferOptions say what the agent does on a schedule for the server that
// would replace it, or as that server.
type TransferOptions struct {
	// ExportSchedule is when an export is written to the backup destination;
	// nil: only on request. ExportsKept is how many are kept.
	ExportSchedule *cron.Schedule
	ExportsKept    int
	// StandbySource is where a standby finds the exports of the server it
	// stands by for; StandbySchedule is when it imports the newest.
	StandbySource   *backup.Storage
	StandbySchedule *cron.Schedule
}

// imageSaver is what an export needs of the runtime beyond the Runtime
// interface: an image written out the way LoadImage reads one.
type imageSaver interface {
	SaveImage(ctx context.Context, image string) (io.ReadCloser, error)
}

// exportOrder is the order applications are exported in, and so the order an
// import deploys them in. An export knows no `after`: that is a shipwick.yaml's.
// What it does know is which applications have a hostname. One without is
// reached by other applications by name — a database, a queue, a worker — and
// goes first; among equals the older application goes first, as it did once.
func exportOrder(apps []store.Application, active map[int64]store.Deployment) []store.Application {
	out := make([]store.Application, 0, len(apps))
	for _, a := range apps {
		if _, ok := active[a.ID]; ok {
			out = append(out, a)
		}
	}
	sort.SliceStable(out, func(i, j int) bool {
		a, b := active[out[i].ID].Spec.Domain == "", active[out[j].ID].Spec.Domain == ""
		if a != b {
			return a
		}
		return out[i].ID < out[j].ID
	})
	return out
}

// deployedApplications loads every application that has an active deployment,
// with that deployment, in export order.
func (e *Engine) deployedApplications(ctx context.Context) ([]store.Application, map[int64]store.Deployment, error) {
	apps, err := e.store.ListApplications(ctx)
	if err != nil {
		return nil, nil, err
	}
	active := map[int64]store.Deployment{}
	for _, a := range apps {
		if a.ActiveDeploymentID == nil {
			continue
		}
		d, err := e.store.GetDeployment(ctx, *a.ActiveDeploymentID)
		if errors.Is(err, store.ErrNotFound) {
			continue // deleted in between
		} else if err != nil {
			return nil, nil, err
		}
		active[a.ID] = d
	}
	return exportOrder(apps, active), active, nil
}

// Export writes an export of the server to w, in clear: w must encrypt. With
// only, the applications named; an unknown name is store.ErrNotFound.
//
// Each application is exported under its lock, like a backup: its volumes are
// read through its container, and what app.json says must be what the
// archives were read from. A deployment asked for meanwhile is told ErrBusy;
// an application that is being deployed fails the export, which names it.
func (e *Engine) Export(ctx context.Context, w io.Writer, only []string) error {
	if !e.beginOp() {
		return ErrShuttingDown
	}
	defer e.opDone()

	apps, _, err := e.deployedApplications(ctx)
	if err != nil {
		return err
	}
	if len(only) > 0 {
		wanted := map[string]bool{}
		for _, name := range only {
			wanted[name] = true
		}
		kept := apps[:0]
		for _, a := range apps {
			if wanted[a.Name] {
				kept = append(kept, a)
				delete(wanted, a.Name)
			}
		}
		for name := range wanted {
			return fmt.Errorf("%s: %w", name, store.ErrNotFound)
		}
		apps = kept
	}

	// Asked before the first byte is written, while the answer can still be
	// an error instead of a file that ends early. The lock taken later is the
	// answer that counts.
	e.mu.Lock()
	for _, a := range apps {
		if held, ok := e.locks[a.Name]; ok && !held.bySupervisor {
			e.mu.Unlock()
			return fmt.Errorf("%s: %w", a.Name, ErrBusy)
		}
	}
	e.mu.Unlock()

	now := time.Now().UTC()
	manifest := exportManifest{Format: exportFormat, CreatedAt: now, Shipwick: version.Version,
		Secrets: []exportSecret{}, Registries: []exportRegistry{}, Certificates: []exportCertificate{}, Applications: []string{}}
	for _, a := range apps {
		manifest.Applications = append(manifest.Applications, a.Name)
	}
	if err := e.exportServerSecrets(ctx, &manifest); err != nil {
		return err
	}

	xw := newExportWriter(w, now)
	if err := xw.json(exportManifestName, manifest); err != nil {
		return err
	}
	var total int64
	for _, a := range apps {
		n, err := e.exportApplication(ctx, xw, a.Name)
		if err != nil {
			return fmt.Errorf("%s: %w", a.Name, err)
		}
		total += n
	}
	if err := xw.close(); err != nil {
		return err
	}
	e.log.Info("export written", "by", actorFrom(ctx), "applications", len(apps), "secrets", len(manifest.Secrets),
		"registries", len(manifest.Registries), "certificates", len(manifest.Certificates), "bytes", total)
	return nil
}

// exportServerSecrets opens what the store keeps sealed, for the manifest.
func (e *Engine) exportServerSecrets(ctx context.Context, m *exportManifest) error {
	secrets, err := e.store.ListSecrets(ctx)
	if err != nil {
		return err
	}
	names := make([]string, 0, len(secrets))
	for _, s := range secrets {
		names = append(names, s.Name)
	}
	values, err := e.store.GetSecrets(ctx, names)
	if err != nil {
		return err
	}
	for _, name := range names {
		if value, ok := values[name]; ok {
			m.Secrets = append(m.Secrets, exportSecret{Name: name, Value: value})
		}
	}

	registries, err := e.store.ListRegistries(ctx)
	if err != nil {
		return err
	}
	for _, r := range registries {
		username, password, found, err := e.store.RegistryCredential(ctx, r.Registry)
		if err != nil {
			return err
		}
		if found {
			m.Registries = append(m.Registries, exportRegistry{Registry: r.Registry, Username: username, Password: password})
		}
	}

	certificates, err := e.store.ListCertificates(ctx)
	if err != nil {
		return err
	}
	for _, c := range certificates {
		m.Certificates = append(m.Certificates, exportCertificate{Hostname: c.Hostname, Certificate: c.CertPEM, Key: c.KeyPEM})
	}
	return nil
}

// exportApplication writes one application and everything that belongs to
// it, and returns the bytes its members held.
func (e *Engine) exportApplication(ctx context.Context, xw *exportWriter, name string) (int64, error) {
	if err := e.lock(ctx, name); err != nil {
		return 0, err
	}
	defer e.unlock(name)

	app, d, err := e.activeDeployment(ctx, name)
	if err != nil {
		return 0, fmt.Errorf("it was deleted or replaced while the export was being written; run the export again: %w", err)
	}
	entry := exportApp{Name: name, Version: d.Version, Spec: d.Spec, Stopped: app.DesiredState == api.DesiredStopped,
		Static: d.StaticDigest != "", Volumes: []string{}}
	for _, v := range d.Spec.Volumes {
		entry.Volumes = append(entry.Volumes, v.Name)
	}

	// The image is opened before app.json is written, because app.json says
	// whether it follows. A daemon that cannot write an image out says so at
	// once; what fails later fails the export.
	var image io.Reader
	if d.StaticDigest == "" && spec.IsLocalImage(d.Spec.Image) {
		entry.Image = &exportImage{}
		rc, err := e.openImage(ctx, d.Spec.Image)
		if err != nil {
			entry.Image.Reason = err.Error()
			e.log.Warn("the image of an application could not be exported", "app", name, "image", d.Spec.Image, "error", err)
		} else {
			defer rc.Close()
			entry.Image.Included, image = true, rc
		}
	}
	if err := xw.json(exportAppBase(name)+"app.json", entry); err != nil {
		return 0, err
	}

	var total int64
	if image != nil {
		n, err := xw.stream(exportImageMember(name), image)
		if err != nil {
			return total, fmt.Errorf("image %s: %w", d.Spec.Image, err)
		}
		total += n
	}
	if entry.Static {
		folder, err := e.openStaticFolder(ctx, d)
		if err != nil {
			return total, err
		}
		n, err := xw.stream(exportStaticMember(name), folder)
		folder.Close()
		if err != nil {
			return total, fmt.Errorf("static folder: %w", err)
		}
		total += n
	}
	if len(d.Spec.Volumes) > 0 {
		n, err := e.exportVolumes(ctx, xw, d)
		if err != nil {
			return total, err
		}
		total += n
	}
	return total, nil
}

// openImage starts writing an image out and waits for its first byte.
func (e *Engine) openImage(ctx context.Context, image string) (io.ReadCloser, error) {
	saver, ok := e.rt.(imageSaver)
	if !ok {
		return nil, errors.New("this runtime cannot write images out")
	}
	rc, err := saver.SaveImage(ctx, image)
	if err != nil {
		return nil, err
	}
	head := bufio.NewReaderSize(rc, 64<<10)
	if _, err := head.Peek(1); err != nil {
		rc.Close()
		if errors.Is(err, io.EOF) {
			return nil, errors.New("the daemon wrote out nothing for it")
		}
		return nil, err
	}
	return struct {
		io.Reader
		io.Closer
	}{head, rc}, nil
}

// openStaticFolder returns the folder a static deployment serves, as the
// archive PUT …/static takes: the upload when the agent still has it, else
// read back from the proxy, which serves it.
func (e *Engine) openStaticFolder(ctx context.Context, d store.Deployment) (io.ReadCloser, error) {
	if e.opts.UploadDir != "" {
		if f, err := os.Open(e.uploadPath(d.Application, d.StaticDigest)); err == nil {
			return f, nil
		}
	}
	proxyID, err := e.rt.ProxyContainer(ctx)
	if err != nil {
		return nil, fmt.Errorf("static folder: %w", err)
	}
	rc, err := e.rt.ExportPath(ctx, proxyID, staticDir(d.Application, d.StaticDigest))
	if err != nil {
		return nil, fmt.Errorf("static folder: %w", err)
	}
	return rc, nil
}

// exportVolumes writes one archive per volume, read through the replica. An
// application that says how its files become safe to copy (`backups.before`,
// `backups.stop`) is taken at its word here as well: an export is a backup
// that travels.
func (e *Engine) exportVolumes(ctx context.Context, xw *exportWriter, d store.Deployment) (int64, error) {
	replicas, err := e.store.ListReplicas(ctx, d.ID)
	if err != nil {
		return 0, err
	}
	if len(replicas) == 0 {
		return 0, errors.New("it has no container to read its volumes through; deploy it again, or leave it out of the export")
	}
	source := replicas[0].ContainerID
	if b := d.Spec.Backups; b != nil {
		c, err := e.rt.InspectContainer(ctx, source)
		if err != nil {
			return 0, err
		}
		if c.Running && len(b.Before) > 0 {
			if err := e.backupBefore(ctx, source, b.Before); err != nil {
				return 0, err
			}
		}
		if c.Running && b.Stop {
			restart, err := e.stopForBackup(ctx, d, replicas)
			defer restart()
			if err != nil {
				return 0, err
			}
		}
	}

	var total int64
	for _, v := range d.Spec.Volumes {
		archive, err := e.rt.ExportPath(ctx, source, v.Path)
		if err != nil {
			return total, fmt.Errorf("volume %s: %w", v.Name, err)
		}
		n, err := xw.stream(exportVolumeMember(d.Application, v.Name), archive)
		archive.Close()
		if err != nil {
			return total, fmt.Errorf("volume %s: %w", v.Name, err)
		}
		total += n
	}
	return total, nil
}

// StartExport writes an export to the backup destination, in the background:
// the directory on the server and, when one is configured, the bucket, where
// a standby finds it. The answer is its record, shaped like a backup's;
// callers poll it until completed_at is set.
func (e *Engine) StartExport(ctx context.Context, trigger string) (api.BackupRun, error) {
	storage := e.opts.Backups.Storage
	switch {
	case storage == nil:
		return api.BackupRun{}, ErrBackupsDisabled
	case !storage.Encrypted():
		return api.BackupRun{}, ErrExportNotEncrypted
	}
	t := e.transfer
	t.mu.Lock()
	if t.exporting {
		t.mu.Unlock()
		return api.BackupRun{}, ErrExportInProgress
	}
	t.exporting = true
	t.mu.Unlock()
	done := func() {
		t.mu.Lock()
		t.exporting = false
		t.mu.Unlock()
	}
	if !e.beginOp() {
		done()
		return api.BackupRun{}, ErrShuttingDown
	}

	// As for a backup: the destination is checked before the run is
	// recorded, because that decides the run's id, and a refusal is recorded
	// as the failure it is.
	refused := e.prepareBackups(ctx)
	run, err := e.store.CreateBackupRun(ctx, ExportOwner, trigger, time.Now())
	if err != nil {
		e.opDone()
		done()
		return api.BackupRun{}, err
	}
	e.log.Info("export started", "export", run.ID, "trigger", trigger)
	actor := actorFrom(ctx)
	go func() {
		defer e.opDone()
		defer done()
		err := refused
		if err == nil {
			err = e.writeExport(WithActor(e.baseCtx, actor), &run)
		}
		e.finishExport(run, err)
	}()
	return backupView(run), nil
}

// writeExport hands the archive to the storage as it is written: the storage
// encrypts, keeps the file and sends it to the bucket.
func (e *Engine) writeExport(ctx context.Context, run *store.BackupRun) error {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(e.Export(ctx, pw, nil))
	}()
	n, err := e.opts.Backups.Storage.Write(ctx, ExportOwner, run.ID, exportFile, pr)
	// Whoever is still writing is told that nobody reads any more.
	pr.CloseWithError(err)
	if err != nil {
		return err
	}
	run.Volumes = []api.BackupVolume{{Volume: exportFile, SizeBytes: n}}
	return nil
}

func (e *Engine) finishExport(run store.BackupRun, failure error) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	storage := e.opts.Backups.Storage
	if failure == nil {
		run.Status, run.Destinations, run.Encrypted = api.BackupSucceeded, storage.Destinations(), storage.Encrypted()
		e.log.Info("export finished", "export", run.ID, "bytes", backupSize(run))
	} else {
		run.Status, run.Volumes, run.Error = api.BackupFailed, []api.BackupVolume{}, failure.Error()
		if e.baseCtx.Err() != nil {
			run.Error = "the agent was shut down while the export was being written"
		}
		e.log.Warn("export failed", "export", run.ID, "error", run.Error)
		if e.backups.isPrepared() {
			if err := e.removeBackupFiles(ctx, ExportOwner, run.ID); err != nil {
				e.log.Warn("could not remove the files of a failed export", "export", run.ID, "error", err)
			}
		}
	}
	if err := e.store.FinishBackupRun(ctx, run, time.Now()); err != nil {
		e.log.Error("could not record the outcome of an export", "export", run.ID, "error", err)
	}
	keep := e.opts.Transfer.ExportsKept
	if keep <= 0 {
		keep = defaultExportsKept
	}
	e.pruneBackups(ctx, ExportOwner, keep)
}

// Exports lists the exports written to the backup destination, newest first.
func (e *Engine) Exports(ctx context.Context, limit int) ([]api.BackupRun, error) {
	return e.listBackups(ctx, ExportOwner, limit)
}

// ExportRun returns one of them.
func (e *Engine) ExportRun(ctx context.Context, id int64) (api.BackupRun, error) {
	run, err := e.store.GetBackupRun(ctx, ExportOwner, id)
	if err != nil {
		return api.BackupRun{}, err
	}
	return backupView(run), nil
}
