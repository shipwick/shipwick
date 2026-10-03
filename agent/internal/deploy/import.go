package deploy

import (
	"archive/tar"
	"bufio"
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// An import reads an export from its first byte to its last and does what it
// finds, in that order: the secrets, registry credentials and certificates
// first, sealed under this server's key; then one application after the
// other, each waited for before the next begins, because the order of the
// archive is the order of dependencies as far as an export knows it (see
// exportOrder).
//
// An application comes into being the one way deployments do: Engine.start.
// What is special happens before its record exists, under its lock, in the
// function that resolves what to deploy: the image is loaded or pulled, and
// the volumes are filled from the archive through a container that is created
// and never started — so the application's first process finds its data, not
// an empty directory to initialise.

// ErrImportInProgress means the server is running an import; it takes one at
// a time.
var ErrImportInProgress = errors.New("an import is running on this server; follow it with: shipwick import --status")

// ImportOptions say how an export is taken in.
type ImportOptions struct {
	// Stopped leaves every application deployed and stopped, as a standby
	// keeps them. It then replaces only applications that are stopped.
	Stopped bool
	// Overwrite replaces what exists under the same name: applications with
	// their volumes, secrets, registry credentials and certificates.
	Overwrite bool
	// Source says where the export came from, for the record.
	Source string
}

// transfer is what the engine remembers about exports and imports between
// requests: the import that runs or ran last, whether an export is being
// written, and how far the two schedules have looked.
type transfer struct {
	mu        sync.Mutex
	importing bool
	last      *api.Import
	seq       int64
	exporting bool

	exportTick  time.Time
	standbyTick time.Time
	pull        api.StandbyPull
}

func newTransfer() *transfer { return &transfer{} }

// importRun is the import in progress. Its record is copied out under the
// transfer's mutex, so that a request sees a consistent one.
type importRun struct {
	e    *Engine
	id   int64
	opts ImportOptions
	rec  api.Import
}

func (im *importRun) update(fn func(rec *api.Import)) {
	t := im.e.transfer
	t.mu.Lock()
	defer t.mu.Unlock()
	fn(&im.rec)
	t.last = copyImport(im.rec)
}

func copyImport(rec api.Import) *api.Import {
	rec.Applications = append([]api.ImportedApplication{}, rec.Applications...)
	rec.Warnings = append([]string{}, rec.Warnings...)
	return &rec
}

func (im *importRun) warn(format string, args ...any) {
	msg := fmt.Sprintf(format, args...)
	im.update(func(rec *api.Import) { rec.Warnings = append(rec.Warnings, msg) })
}

// application changes the record of one application.
func (im *importRun) application(name string, fn func(a *api.ImportedApplication)) {
	im.update(func(rec *api.Import) {
		for i := range rec.Applications {
			if rec.Applications[i].Name == name {
				fn(&rec.Applications[i])
				return
			}
		}
		a := api.ImportedApplication{Name: name, Status: api.ImportAppPending, Volumes: []string{}}
		fn(&a)
		rec.Applications = append(rec.Applications, a)
	})
}

// ImportStatus returns the import that is running, or ran last; ok is false
// when this agent has run none since it started.
func (e *Engine) ImportStatus() (api.Import, bool) {
	t := e.transfer
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		return api.Import{}, false
	}
	return *copyImport(*t.last), true
}

// beginImport claims the server for an import.
func (e *Engine) beginImport(opts ImportOptions) (*importRun, error) {
	t := e.transfer
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.importing {
		return nil, ErrImportInProgress
	}
	if !e.beginOp() {
		return nil, ErrShuttingDown
	}
	t.importing = true
	t.seq++
	im := &importRun{e: e, id: time.Now().Unix()*1000 + t.seq%1000, opts: opts, rec: api.Import{
		Status: api.ImportRunning, Source: opts.Source, Stopped: opts.Stopped, Overwrite: opts.Overwrite,
		StartedAt: time.Now().UTC(), Applications: []api.ImportedApplication{}, Warnings: []string{},
	}}
	t.last = copyImport(im.rec)
	return im, nil
}

// Import takes in an export, read in clear from archive, and returns when
// every application in it has been dealt with. What happened to each is in
// the answer and, while it runs, in ImportStatus. The error is set when the
// import could not go on — the archive is not an export, or breaks off; an
// application that could not be imported is not that, and the rest goes on.
func (e *Engine) Import(ctx context.Context, archive io.Reader, opts ImportOptions) (api.Import, error) {
	im, err := e.beginImport(opts)
	if err != nil {
		return api.Import{}, err
	}
	return im.run(ctx, archive)
}

func (im *importRun) run(ctx context.Context, archive io.Reader) (api.Import, error) {
	e := im.e
	defer e.opDone()
	e.log.Info("import started", "by", actorFrom(ctx), "source", im.opts.Source, "stopped", im.opts.Stopped, "overwrite", im.opts.Overwrite)

	err := im.read(ctx, newExportReader(archive))
	if err != nil && ctx.Err() != nil {
		err = errors.New("the import was interrupted: the upload ended or the agent is shutting down. Run it again with --overwrite; what it had finished is in place")
	}

	t := e.transfer
	t.mu.Lock()
	now := time.Now().UTC()
	im.rec.CompletedAt = &now
	im.rec.Status = api.ImportSucceeded
	for i, a := range im.rec.Applications {
		switch a.Status {
		case api.ImportAppFailed:
			im.rec.Status = api.ImportFailed
		case api.ImportAppPending, api.ImportAppRunning:
			im.rec.Applications[i].Status = api.ImportAppFailed
			im.rec.Applications[i].Message = "the import ended before it was reached"
		}
	}
	if err != nil {
		im.rec.Status, im.rec.Error = api.ImportFailed, err.Error()
	}
	t.last = copyImport(im.rec)
	t.importing = false
	final := *copyImport(im.rec)
	t.mu.Unlock()

	e.log.Info("import finished", "status", final.Status, "applications", len(final.Applications), "error", final.Error)
	return final, err
}

// read goes through the archive.
func (im *importRun) read(ctx context.Context, xr *exportReader) error {
	e := im.e
	var m exportManifest
	if err := xr.json(exportManifestName, &m); err != nil {
		var invalid *InvalidExportError
		if !errors.As(err, &invalid) {
			return err
		}
		if strings.Contains(invalid.Reason, "passphrase") {
			return err
		}
		return &InvalidExportError{Reason: "this is not an export: it does not begin with " + exportManifestName + ". Write one with: shipwick export"}
	}
	if m.Format != exportFormat {
		return &InvalidExportError{Reason: fmt.Sprintf("the export is of format %d, written by Shipwick %s; this agent reads format %d. Upgrade the agent, or export with a matching version", m.Format, m.Shipwick, exportFormat)}
	}
	im.update(func(rec *api.Import) {
		created := m.CreatedAt
		rec.ExportedAt = &created
		for _, name := range m.Applications {
			rec.Applications = append(rec.Applications, api.ImportedApplication{Name: name, Status: api.ImportAppPending, Volumes: []string{}})
		}
	})

	if err := im.serverSecrets(ctx, m); err != nil {
		return err
	}

	for {
		hdr, err := xr.peek()
		if errors.Is(err, io.EOF) {
			return nil
		} else if err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
		name, ok := exportAppName(hdr.Name)
		if !ok {
			return damagedExport("%s is not where it belongs", hdr.Name)
		}
		var entry exportApp
		if err := xr.json(hdr.Name, &entry); err != nil {
			return err
		}
		im.application(name, func(a *api.ImportedApplication) { a.Status, a.Version = api.ImportAppRunning, entry.Version })

		status, message, fatal := im.importApplication(ctx, xr, name, entry)
		if fatal != nil {
			im.application(name, func(a *api.ImportedApplication) {
				a.Status, a.Message = api.ImportAppFailed, fatal.Error()
			})
			return fatal
		}
		im.application(name, func(a *api.ImportedApplication) { a.Status, a.Message = status, message })
		if status != api.ImportAppImported {
			e.log.Warn("application not imported", "app", name, "status", status, "reason", message)
		}
		if err := xr.skipApplication(); err != nil {
			return err
		}
	}
}

// serverSecrets stores the secrets, registry credentials and certificates of
// the export, sealed under this server's key by the store. What exists under
// the same name is kept unless the import overwrites.
func (im *importRun) serverSecrets(ctx context.Context, m exportManifest) error {
	e := im.e
	now := time.Now()
	overwrite := im.opts.Overwrite
	if overwrite && im.promoted(ctx, m.Applications) {
		overwrite = false
		im.warn("this server runs applications of the export, so the secrets, registry credentials and certificates it has were kept. If it has been promoted, stop importing into it")
	}

	existing, err := e.store.ListSecrets(ctx)
	if err != nil {
		return err
	}
	have := map[string]bool{}
	for _, s := range existing {
		have[s.Name] = true
	}
	var kept []string
	n := 0
	for _, s := range m.Secrets {
		if api.ValidateSecretName(s.Name) != nil || api.ValidateSecretValue(s.Value) != nil {
			im.warn("secret %s was not imported: its name or value is not one this agent accepts", printable(s.Name))
			continue
		}
		if have[s.Name] && !overwrite {
			kept = append(kept, s.Name)
			continue
		}
		if err := e.store.SetSecret(ctx, s.Name, s.Value, now); err != nil {
			if errors.Is(err, store.ErrTooManySecrets) {
				im.warn("secret %s was not imported: %v", s.Name, err)
				continue
			}
			return err
		}
		n++
	}
	if len(kept) > 0 && overwrite == im.opts.Overwrite {
		im.warn("%s: a secret by that name exists on this server and was kept; --overwrite replaces it", strings.Join(kept, ", "))
	}
	im.update(func(rec *api.Import) { rec.Secrets = n })

	registries, err := e.store.ListRegistries(ctx)
	if err != nil {
		return err
	}
	have = map[string]bool{}
	for _, r := range registries {
		have[r.Registry] = true
	}
	n = 0
	for _, r := range m.Registries {
		registry, _, rerr := docker.ImageRegistry(r.Registry + "/x")
		if rerr != nil || registry != r.Registry || api.ValidateRegistryCredential(r.Username, r.Password) != nil {
			im.warn("the credential for registry %s was not imported: it is not one this agent accepts", printable(r.Registry))
			continue
		}
		if have[r.Registry] && !overwrite {
			if overwrite == im.opts.Overwrite {
				im.warn("%s: a credential for that registry exists on this server and was kept; --overwrite replaces it", r.Registry)
			}
			continue
		}
		// Not checked against the registry, as `shipwick registry login`
		// would: it worked on the server it comes from, and the pull that
		// follows is the check.
		if err := e.store.SetRegistry(ctx, r.Registry, r.Username, r.Password, now); err != nil {
			im.warn("the credential for registry %s was not imported: %v", r.Registry, err)
			continue
		}
		n++
	}
	im.update(func(rec *api.Import) { rec.Registries = n })

	certificates, err := e.Certificates(ctx)
	if err != nil {
		return err
	}
	have = map[string]bool{}
	for _, c := range certificates {
		have[c.Hostname] = true
	}
	n = 0
	for _, c := range m.Certificates {
		if spec.ValidateHostname(c.Hostname) != nil {
			im.warn("the certificate for %s was not imported: that is not a hostname", printable(c.Hostname))
			continue
		}
		if have[c.Hostname] && !overwrite {
			if overwrite == im.opts.Overwrite {
				im.warn("%s: a certificate for that hostname exists on this server and was kept; --overwrite replaces it", c.Hostname)
			}
			continue
		}
		// SetCertificate checks it like `shipwick certs add` does; one that
		// has expired since is refused, in a sentence that quotes none of it.
		if _, err := e.SetCertificate(ctx, c.Hostname, c.Certificate, c.Key); err != nil {
			im.warn("the certificate for %s was not imported: %v", c.Hostname, err)
			continue
		}
		n++
	}
	im.update(func(rec *api.Import) { rec.Certificates = n })
	return nil
}

// printable bounds and cleans a name that failed validation before it is
// repeated in a message.
func printable(s string) string {
	if len(s) > 64 {
		s = s[:64] + "…"
	}
	return strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return '?'
		}
		return r
	}, s)
}

// checkImported validates again what an export says about an application:
// the archive was written by an agent, but it arrives over the API like a
// deploy.yaml, and the same names end up in containers, URLs and the proxy's
// configuration. pkg/spec validates documents, not parsed configurations, so
// this applies its validators to the fields that travel that far.
func checkImported(name string, entry exportApp) error {
	a := entry.Spec
	if entry.Name != name || a.Name != name {
		return errors.New("its configuration is filed under another application's name")
	}
	if err := spec.ValidateName(name); err != nil {
		return err
	}
	if a.Static == nil {
		if err := spec.ValidateImage(a.Image); err != nil {
			return fmt.Errorf("image: %w", err)
		}
	}
	if entry.Static != (a.Static != nil) {
		return errors.New("its configuration and its files do not agree on whether it is a static application")
	}
	if a.Domain != "" {
		if err := spec.ValidateHostname(a.Domain); err != nil {
			return fmt.Errorf("domain: %w", err)
		}
	}
	for _, h := range append(append([]string{}, a.Aliases...), a.Redirects...) {
		if err := spec.ValidateHostname(h); err != nil {
			return fmt.Errorf("hostname %s: %w", printable(h), err)
		}
	}
	if a.Replicas < 1 || a.Port < 0 || a.Port > 65535 {
		return errors.New("replicas or port are out of range")
	}
	if a.Deploy.Strategy != spec.StrategyRolling && a.Deploy.Strategy != spec.StrategyRecreate {
		return errors.New("deploy.strategy is not one this agent knows")
	}
	mounted := map[string]bool{}
	for _, v := range a.Volumes {
		// The name becomes a Docker volume's name and a member of the archive.
		if spec.ValidateName(v.Name) != nil || !path.IsAbs(v.Path) || path.Clean(v.Path) != v.Path || v.Path == "/" {
			return fmt.Errorf("volume %s is not one this agent accepts", printable(v.Name))
		}
		mounted[v.Name] = true
	}
	if len(a.Volumes) > 0 && (a.Replicas != 1 || a.Deploy.Strategy != spec.StrategyRecreate) {
		return errors.New("an application with volumes has one replica and the recreate strategy")
	}
	if len(entry.Volumes) != len(a.Volumes) {
		return errors.New("its configuration and its archives do not name the same volumes")
	}
	for _, v := range entry.Volumes {
		if !mounted[v] {
			return errors.New("its configuration and its archives do not name the same volumes")
		}
	}
	for _, argv := range [][]string{a.Entrypoint, a.Command} {
		if len(argv) > 0 {
			if err := spec.ValidateCommand(argv); err != nil {
				return err
			}
		}
	}
	for _, j := range a.Jobs {
		if err := spec.ValidateJobName(j.Name); err != nil {
			return err
		}
		if err := spec.ValidateCommand(j.Command); err != nil {
			return fmt.Errorf("job %s: %w", j.Name, err)
		}
	}
	return nil
}

var (
	errImportExists  = errors.New("it exists on this server and was left as it is; import with --overwrite to replace it and its volumes")
	errImportRunning = errors.New("it is running on this server; an import that leaves applications stopped replaces only stopped ones. If this server has been promoted, stop importing into it")
)

// importApplication imports one application whose app.json has been read.
// It reports what became of it and why; fatal is set when the archive itself
// cannot be read on.
func (im *importRun) importApplication(ctx context.Context, xr *exportReader, name string, entry exportApp) (status, message string, fatal error) {
	e := im.e
	if err := checkImported(name, entry); err != nil {
		return api.ImportAppFailed, "the export's entry for it is refused: " + err.Error(), nil
	}
	app := entry.Spec
	dormant := im.opts.Stopped || entry.Stopped

	// Asked before anything of it is read, so that what is skipped costs
	// nothing; asked again under the lock, which is the answer that counts.
	if err := im.mayReplace(ctx, name); err != nil {
		return api.ImportAppSkipped, err.Error(), nil
	}

	if entry.Image != nil {
		if !entry.Image.Included {
			if here, err := e.rt.ImageExists(ctx, app.Image); err != nil || !here {
				return api.ImportAppSkipped, fmt.Sprintf("its image was built by shipwick deploy and could not be written out by the old server (%s). Deploy it to this server from its project with shipwick deploy, stop it, and bring its volumes over with shipwick volumes backup and shipwick volumes restore", entry.Image.Reason), nil
			}
		} else {
			member, err := xr.stream(exportImageMember(name))
			if err != nil {
				return "", "", err
			}
			_, err = e.LoadImage(ctx, name, member)
			if derr := member.drain(); derr != nil {
				return "", "", derr
			}
			if err != nil {
				return api.ImportAppFailed, fmt.Sprintf("its image could not be loaded: %v", err), nil
			}
		}
	}

	var static *store.StaticFiles
	if entry.Static {
		member, err := xr.stream(exportStaticMember(name))
		if err != nil {
			return "", "", err
		}
		upload, err := e.StoreStatic(ctx, name, member)
		if derr := member.drain(); derr != nil {
			return "", "", derr
		}
		if err != nil {
			return api.ImportAppFailed, fmt.Sprintf("its folder could not be stored: %v", err), nil
		}
		static = &store.StaticFiles{Digest: upload.Digest, Files: upload.Files, Bytes: upload.SizeBytes}
	}

	kind := api.KindImport
	if im.opts.Stopped {
		kind = api.KindStandby
	}
	var restored []string
	d, err := e.start(ctx, name, func(ctx context.Context) (origin, error) {
		existing, err := im.replaceable(ctx, name)
		if err != nil {
			return origin{}, err
		}
		// Before anything of the old one is touched: a hostname another
		// application has, a port that is taken.
		if err := e.admit(ctx, app, false); err != nil {
			return origin{}, err
		}
		if len(entry.Volumes) > 0 {
			if restored, err = im.restoreVolumes(ctx, xr, existing, app, entry); err != nil {
				return origin{}, err
			}
		}
		return origin{spec: app, kind: kind, static: static, dormant: dormant}, nil
	})
	var invalid *InvalidExportError
	switch {
	case errors.Is(err, errImportExists), errors.Is(err, errImportRunning):
		return api.ImportAppSkipped, err.Error(), nil
	case errors.As(err, &invalid):
		return "", "", err
	case err != nil:
		return api.ImportAppFailed, importFailure(err), nil
	}
	im.application(name, func(a *api.ImportedApplication) {
		id := d.ID
		a.DeploymentID, a.Version, a.Volumes = &id, d.Version, append([]string{}, restored...)
	})
	if len(restored) > 0 {
		e.step(ctx, &d, "Imported from the export of %s; %s restored before the first start", im.exportedAt(), plural(len(restored), "volume"))
	} else {
		e.step(ctx, &d, "Imported from the export of %s", im.exportedAt())
	}

	final, err := e.awaitDeployment(ctx, d)
	switch {
	case err != nil:
		return api.ImportAppFailed, fmt.Sprintf("its deployment #%d was not waited for: %v; see it with: shipwick status %s", d.ID, err, name), nil
	case final.Status != api.StatusActive:
		return api.ImportAppFailed, fmt.Sprintf("its deployment #%d ended %s: %s", final.ID, final.Status, final.Error), nil
	}
	return api.ImportAppImported, "", nil
}

func (im *importRun) exportedAt() string {
	if im.rec.ExportedAt == nil {
		return "an unknown time"
	}
	return im.rec.ExportedAt.Format("2006-01-02 15:04 UTC")
}

// importFailure words an error of Engine.start for the import's record. A
// refusal that would be a validation error of a deploy.yaml names its field.
func importFailure(err error) string {
	var fields interface{ Fields() []spec.FieldError }
	var conflict *DomainConflictError
	var port *PortConflictError
	switch {
	case errors.As(err, &conflict):
		return fmt.Sprintf("%s: %s", conflict.Field, conflict.Message())
	case errors.As(err, &port):
		return fmt.Sprintf("%s: already published by %s", port.Field(), port.Owner)
	case errors.As(err, &fields):
		var parts []string
		for _, f := range fields.Fields() {
			parts = append(parts, f.Field+": "+f.Message)
		}
		return strings.Join(parts, "; ")
	case errors.Is(err, ErrBusy):
		return "another operation is in progress for it on this server; import again when it is done"
	}
	return err.Error()
}

// mayReplace says whether the import may put the application in place of
// what the server has under its name.
func (im *importRun) mayReplace(ctx context.Context, name string) error {
	_, err := im.replaceable(ctx, name)
	return err
}

// replaceable returns the application the import would replace, nil when
// there is none, and refuses when it may not.
func (im *importRun) replaceable(ctx context.Context, name string) (*store.Application, error) {
	existing, err := im.e.store.GetApplication(ctx, name)
	if errors.Is(err, store.ErrNotFound) {
		return nil, nil
	} else if err != nil {
		return nil, err
	}
	if existing.ActiveDeploymentID == nil {
		// Known by name only: every deployment of it failed. Nothing runs
		// and nothing is lost.
		return &existing, nil
	}
	if !im.opts.Overwrite {
		return nil, errImportExists
	}
	// The rule that makes a scheduled standby import safe to leave running:
	// once a standby has been promoted, its applications run, and the next
	// export of the old server must not stop them and put old data in.
	if im.opts.Stopped && existing.DesiredState == api.DesiredRunning {
		return nil, errImportRunning
	}
	return &existing, nil
}

// restoreVolumes fills the application's volumes from the archive before the
// application exists as a deployment. The caller holds its lock. What the
// server has of the application — containers and volumes — goes first: a
// volume cannot be replaced while a container mounts it, and an archive
// extracted over files that are there would be a mix of two states.
func (im *importRun) restoreVolumes(ctx context.Context, xr *exportReader, existing *store.Application, app spec.App, entry exportApp) ([]string, error) {
	e, name := im.e, app.Name
	remover, ok := e.rt.(volumeRemover)
	if !ok {
		return nil, errors.New("this runtime cannot remove volumes")
	}
	if !im.opts.Overwrite {
		// A volume outlives its application on purpose, so one may be here
		// although the application is not.
		present, err := e.rt.ListVolumes(ctx)
		if err != nil {
			return nil, err
		}
		for _, v := range present {
			if v.App == name {
				return nil, fmt.Errorf("volume %s exists on this server, left by an application of the same name; remove it with shipwick volumes rm %s, or import with --overwrite to replace it", v.Volume, v.Name)
			}
		}
	}
	// The image first: nothing is removed for an application that cannot be
	// created afterwards.
	if !spec.IsLocalImage(app.Image) {
		if err := e.pull(ctx, app.Image); err != nil {
			if here, ierr := e.rt.ImageExists(ctx, app.Image); ierr != nil || !here {
				return nil, err
			}
		}
	}

	if existing != nil {
		if err := im.clear(ctx, *existing); err != nil {
			return nil, err
		}
	}
	paths := map[string]string{}
	cspec := docker.ContainerSpec{App: name, Image: app.Image, Job: &docker.JobSpec{Name: docker.ImportJob, RunID: im.id}}
	for _, v := range app.Volumes {
		if err := remover.RemoveVolume(ctx, name, v.Name); err != nil {
			return nil, err
		}
		paths[v.Name] = v.Path
		cspec.Mounts = append(cspec.Mounts, docker.Mount{Volume: v.Name, Path: v.Path})
	}

	// A job as far as everything that manages replicas is concerned, and
	// never started: it exists to give Docker's archive endpoint a
	// filesystem with the volumes in it.
	id, _, err := e.createContainer(ctx, cspec)
	if err != nil {
		return nil, fmt.Errorf("the container the volumes are filled through could not be created: %w", err)
	}
	defer func() {
		cleanup, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if err := e.rt.RemoveContainer(cleanup, id); err != nil {
			e.log.Warn("could not remove the container an import filled volumes through", "app", name, "container", id, "error", err)
		}
	}()

	var restored []string
	for _, v := range entry.Volumes {
		member, err := xr.stream(exportVolumeMember(name, v))
		if err != nil {
			return restored, err
		}
		head := bufio.NewReaderSize(member, 64<<10)
		first, perr := head.Peek(512)
		if perr != nil && !errors.Is(perr, io.EOF) {
			return restored, perr
		}
		if _, err := tar.NewReader(bytes.NewReader(first)).Next(); err != nil && !errors.Is(err, io.EOF) {
			return restored, damagedExport("the archive of volume %s of %s is not a tar file", v, name)
		}
		err = e.rt.ImportPath(ctx, id, paths[v], head)
		if derr := member.drain(); derr != nil {
			return restored, derr
		}
		if err != nil {
			return restored, fmt.Errorf("volume %s could not be restored: %w", v, err)
		}
		restored = append(restored, v)
	}
	return restored, nil
}

// clear removes the containers of an application that is about to be
// replaced with its volumes. It is recorded as stopped first, as Stop does
// it, and taken out of the proxy before its processes are signalled.
func (im *importRun) clear(ctx context.Context, app store.Application) error {
	e := im.e
	if err := e.store.SetDesiredState(ctx, app.ID, api.DesiredStopped, time.Now()); err != nil {
		return err
	}
	e.syncProxyBestEffort(ctx, app.Name)
	listed, err := e.rt.ListContainers(ctx, app.Name)
	if err != nil {
		return err
	}
	var containers []docker.Container
	for _, c := range listed {
		if !e.draining(c.ID) {
			containers = append(containers, c)
		}
	}
	for i, err := range e.retireAll(ctx, containers) {
		if err != nil {
			return fmt.Errorf("remove container %s: %w", containers[i].Name, err)
		}
	}
	return e.awaitDrains(ctx, app.Name)
}

// awaitDeployment waits until the deployment has completed and returns it as
// it ended. The deployment holds its application's lock until just before,
// so the release of that lock is what is waited for; the stamp follows it
// within moments.
func (e *Engine) awaitDeployment(ctx context.Context, d store.Deployment) (store.Deployment, error) {
	e.mu.Lock()
	held := e.locks[d.Application]
	e.mu.Unlock()
	if held != nil {
		select {
		case <-held.released:
		case <-ctx.Done():
			return d, ctx.Err()
		}
	}
	poll := time.NewTicker(10 * time.Millisecond)
	defer poll.Stop()
	for {
		current, err := e.store.GetDeployment(ctx, d.ID)
		if err != nil {
			return d, err
		}
		if current.CompletedAt != nil {
			return current, nil
		}
		if e.baseCtx.Err() != nil {
			return current, ErrShuttingDown
		}
		select {
		case <-poll.C:
		case <-ctx.Done():
			return current, ctx.Err()
		}
	}
}

// promoted reports whether this server runs an application the export names.
// A standby that does has taken over; what the old server still exports is
// then older than what is here.
func (im *importRun) promoted(ctx context.Context, names []string) bool {
	if !im.opts.Stopped {
		return false
	}
	for _, name := range names {
		app, err := im.e.store.GetApplication(ctx, name)
		if err == nil && app.ActiveDeploymentID != nil && app.DesiredState == api.DesiredRunning {
			return true
		}
	}
	return false
}
