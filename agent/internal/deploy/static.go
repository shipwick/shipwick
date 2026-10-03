package deploy

import (
	"archive/tar"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// A static application is a folder the proxy serves itself; no container is
// ever created for it. Deploying one is two requests: the CLI uploads the
// folder as a tar archive, which the agent keeps in its data directory under
// the archive's digest, then submits the deploy.yaml naming that digest. The
// rollout copies the files into the proxy's own container — the directory
// /srv/shipwick/<app>/<digest>, on a volume of the compose setup — checks
// that index.html is there, and routes the domain to the directory.
//
// Directories are named by content, not by deployment: a rollback, or a
// redeploy of the same folder, routes to a directory that already exists and
// needs no upload; the upload is read only for files the proxy has not seen.
// Two directories are kept per application, the one serving and the one it
// replaced, like the images of container applications.

const (
	// staticRoot is where the proxy's container holds the folders. The compose
	// files mount a volume there.
	staticRoot = "/srv/shipwick"
	// staticExecTimeout bounds the commands run inside the proxy: a test, a
	// rename, a removal.
	staticExecTimeout = 30 * time.Second
)

// ErrStaticApplication means an operation that needs containers was asked of
// an application the proxy serves from a folder.
var ErrStaticApplication = errors.New("this application is a folder served by the proxy; it has no containers")

// ErrNoUpload means a deployment named an upload the agent does not have.
var ErrNoUpload = errors.New("no files were uploaded for this deployment: run shipwick deploy from the project")

// InvalidUploadError means the uploaded body is not what a static folder's
// archive must be.
type InvalidUploadError struct{ Reason string }

func (e *InvalidUploadError) Error() string { return e.Reason }

var staticDigestPattern = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// ValidStaticDigest reports whether s is a digest as PUT …/static answers it.
func ValidStaticDigest(s string) bool { return staticDigestPattern.MatchString(s) }

// staticDir is the directory in the proxy that serves an application's
// folder of the given digest. Both parts are validated before they get here:
// the name by pkg/spec, the digest by ValidStaticDigest.
func staticDir(app, digest string) string {
	return path.Join(staticRoot, app, strings.TrimPrefix(digest, "sha256:"))
}

// staticOf is the static part of a stored deployment, for a redeploy or a
// rollback that re-uses it; nil for a deployment that runs containers.
func staticOf(d store.Deployment) *store.StaticFiles {
	if d.StaticDigest == "" {
		return nil
	}
	return &store.StaticFiles{Digest: d.StaticDigest, Files: d.StaticFiles, Bytes: d.StaticBytes}
}

// StoreStatic reads the archive of a static application's folder, keeps it
// for the deployment that names it, and answers with its digest. One archive
// is kept per application: the one just received replaces the last.
//
// The archive is inspected while it is written: only files and directories,
// none of them outside the folder. A symbolic link would be followed by the
// proxy's file server, and the proxy's container holds the certificates.
func (e *Engine) StoreStatic(ctx context.Context, name string, archive io.Reader) (api.StaticUpload, error) {
	if e.opts.UploadDir == "" {
		return api.StaticUpload{}, errors.New("this agent has nowhere to keep uploads: it was started without a data directory")
	}
	// The name becomes a directory: checked here, whoever checked it before.
	if err := spec.ValidateName(name); err != nil {
		return api.StaticUpload{}, &InvalidUploadError{Reason: "the application's name is not one: " + err.Error()}
	}
	// Under the application's lock: a deployment reading the last upload must
	// not find it replaced half-way through.
	if err := e.lock(ctx, name); err != nil {
		return api.StaticUpload{}, err
	}
	defer e.unlock(name)

	dir := filepath.Join(e.opts.UploadDir, name)
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return api.StaticUpload{}, fmt.Errorf("create upload directory: %w", err)
	}
	tmp, err := os.CreateTemp(dir, ".upload-*")
	if err != nil {
		return api.StaticUpload{}, fmt.Errorf("create upload file: %w", err)
	}
	defer os.Remove(tmp.Name()) // gone by then when the upload was kept
	defer tmp.Close()

	sum := sha256.New()
	body := io.TeeReader(archive, io.MultiWriter(tmp, sum))
	files, err := inspectStaticArchive(body)
	if err != nil {
		return api.StaticUpload{}, err
	}
	if err := tmp.Close(); err != nil {
		return api.StaticUpload{}, fmt.Errorf("write upload: %w", err)
	}
	files.Digest = "sha256:" + hex.EncodeToString(sum.Sum(nil))

	// The archive and a note of what is in it, named by the digest; the
	// deployment that follows looks both up by it.
	if err := os.Rename(tmp.Name(), e.uploadPath(name, files.Digest)); err != nil {
		return api.StaticUpload{}, fmt.Errorf("keep upload: %w", err)
	}
	note, _ := json.Marshal(files)
	if err := os.WriteFile(e.uploadNotePath(name, files.Digest), note, 0o600); err != nil {
		return api.StaticUpload{}, fmt.Errorf("keep upload: %w", err)
	}
	e.pruneUploads(name, files.Digest)
	e.log.Info("static folder uploaded", "app", name, "digest", files.Digest, "files", files.Files, "bytes", files.Bytes)
	return api.StaticUpload{Digest: files.Digest, SizeBytes: files.Bytes, Files: files.Files}, nil
}

// inspectStaticArchive reads the archive to its end — the caller hashes and
// stores what it reads — and counts what it holds, refusing what a served
// folder may not contain.
func inspectStaticArchive(r io.Reader) (store.StaticFiles, error) {
	var files store.StaticFiles
	tr := tar.NewReader(r)
	for {
		hdr, err := tr.Next()
		if errors.Is(err, io.EOF) {
			break
		}
		if err != nil {
			return files, &InvalidUploadError{Reason: "the body is not a tar archive: " + err.Error()}
		}
		name := path.Clean(hdr.Name)
		if name == "." {
			continue // the folder itself
		}
		if path.IsAbs(name) || name == ".." || strings.HasPrefix(name, "../") {
			return files, &InvalidUploadError{Reason: fmt.Sprintf("entry %q is outside the folder", hdr.Name)}
		}
		switch hdr.Typeflag {
		case tar.TypeReg:
			files.Files++
			files.Bytes += hdr.Size
		case tar.TypeDir, tar.TypeXGlobalHeader:
		default:
			return files, &InvalidUploadError{Reason: fmt.Sprintf("entry %q is not a file or a directory; a served folder may hold nothing else", hdr.Name)}
		}
	}
	// Past the end-of-archive marker there is padding, which is part of what
	// was sent and so of the digest.
	if _, err := io.Copy(io.Discard, r); err != nil {
		return files, fmt.Errorf("read upload: %w", err)
	}
	if files.Files == 0 {
		return files, &InvalidUploadError{Reason: "the folder holds no files"}
	}
	return files, nil
}

func (e *Engine) uploadPath(app, digest string) string {
	return filepath.Join(e.opts.UploadDir, app, strings.TrimPrefix(digest, "sha256:")+".tar")
}

func (e *Engine) uploadNotePath(app, digest string) string {
	return filepath.Join(e.opts.UploadDir, app, strings.TrimPrefix(digest, "sha256:")+".json")
}

// uploadInfo reads what an upload holds, for the deployment record.
func (e *Engine) uploadInfo(app, digest string) (store.StaticFiles, error) {
	// Both become parts of a path.
	if spec.ValidateName(app) != nil || !ValidStaticDigest(digest) {
		return store.StaticFiles{}, ErrNoUpload
	}
	note, err := os.ReadFile(e.uploadNotePath(app, digest))
	if err != nil {
		return store.StaticFiles{}, ErrNoUpload
	}
	var files store.StaticFiles
	if err := json.Unmarshal(note, &files); err != nil || files.Digest != digest {
		return store.StaticFiles{}, ErrNoUpload
	}
	return files, nil
}

// pruneUploads removes every upload of the application but the one named:
// earlier archives, and the file of an upload that was cut off.
func (e *Engine) pruneUploads(app, keep string) {
	dir := filepath.Join(e.opts.UploadDir, app)
	entries, err := os.ReadDir(dir)
	if err != nil {
		return
	}
	hex := strings.TrimPrefix(keep, "sha256:")
	for _, entry := range entries {
		if entry.Name() == hex+".tar" || entry.Name() == hex+".json" {
			continue
		}
		os.Remove(filepath.Join(dir, entry.Name()))
	}
}

// DeployStatic records a deployment of a static application and runs it in
// the background, like Deploy; digest names the upload it serves.
func (e *Engine) DeployStatic(ctx context.Context, app spec.App, digest string) (store.Deployment, error) {
	if app.Static == nil {
		return store.Deployment{}, errors.New("not a static application")
	}
	if !ValidStaticDigest(digest) {
		return store.Deployment{}, &InvalidUploadError{Reason: "the digest must be sha256: followed by 64 hex characters, as the upload answered it"}
	}
	return e.start(ctx, app.Name, func(ctx context.Context) (origin, error) {
		files, err := e.uploadInfo(app.Name, digest)
		if err != nil {
			return origin{}, err
		}
		// A static application has no env, but its proxy block may hold
		// passwords left for the server to fill in.
		app, err := e.resolveSecrets(ctx, app)
		if err != nil {
			return origin{}, err
		}
		return origin{spec: app, kind: api.KindDeploy, static: &files}, nil
	})
}

// executeStatic is the rollout of a static application. It has no replicas to
// start or verify: the files are put into the proxy, checked, and routed to.
// A failure removes what this rollout put there and leaves routing where it
// was, which abort does for every rollout that retired nothing.
func (r *rollout) executeStatic(ctx context.Context) error {
	err := r.serveStatic(ctx)
	if err != nil && r.staticPart != "" {
		cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
		defer cancel()
		if proxyID, perr := r.e.rt.ProxyContainer(cleanupCtx); perr == nil {
			r.e.proxyExec(cleanupCtx, proxyID, "rm", "-rf", r.staticPart)
		}
	}
	return err
}

func (r *rollout) serveStatic(ctx context.Context) error {
	e, d := r.e, r.d
	dir := staticDir(d.Application, d.StaticDigest)
	what := fmt.Sprintf("%s (%s)", plural(d.StaticFiles, "file"), spec.FormatMemory(d.StaticBytes))

	if err := r.reach(ctx, api.StatusBuilding); err != nil {
		return err
	}
	proxyID, err := e.rt.ProxyContainer(ctx)
	if err != nil {
		return err
	}
	present, err := e.proxyDirExists(ctx, proxyID, dir)
	if err != nil {
		return err
	}
	upload := e.uploadPath(d.Application, d.StaticDigest)
	switch {
	case present:
		e.step(ctx, d, "The proxy already has %s", what)
	default:
		if _, err := os.Stat(upload); err != nil {
			if d.Kind == api.KindDeploy {
				return ErrNoUpload
			}
			return fmt.Errorf("the files of %s are no longer on the server: deploy the folder again", d.Version)
		}
		e.step(ctx, d, "Received %s", what)
	}
	// What is being replaced, for the messages; routing follows the database
	// until the switch, and the database names it until the commit.
	if app, err := e.store.GetApplication(ctx, d.Application); err == nil && app.ActiveDeploymentID != nil {
		if prev, err := e.store.GetDeployment(ctx, *app.ActiveDeploymentID); err == nil {
			r.prev = &prev
		}
	}

	if err := r.reach(ctx, api.StatusStarting); err != nil {
		return err
	}
	if !present {
		// Extracted next to its final name and renamed once it is checked, so
		// that a crash half-way never leaves a directory that looks complete.
		part := dir + ".part"
		if err := e.proxyExec(ctx, proxyID, "rm", "-rf", part); err != nil {
			return err
		}
		if err := e.proxyExec(ctx, proxyID, "mkdir", "-p", part); err != nil {
			return err
		}
		r.staticPart = part
		f, err := os.Open(upload)
		if err != nil {
			return ErrNoUpload
		}
		err = e.rt.ImportPath(ctx, proxyID, part, f)
		f.Close()
		if err != nil {
			return fmt.Errorf("could not copy the files into the proxy: %w", err)
		}
		e.step(ctx, d, "Copied %s into the proxy", plural(d.StaticFiles, "file"))
	}

	if err := r.reach(ctx, api.StatusHealthChecking); err != nil {
		return err
	}
	check := dir
	if !present {
		check = r.staticPart
	}
	code, _, err := e.rt.Exec(ctx, proxyID, []string{"test", "-f", check + "/index.html"}, staticExecTimeout)
	if err != nil {
		return fmt.Errorf("could not look for index.html in the proxy: %w", err)
	}
	if code != 0 {
		return errors.New("the folder has no index.html: build the site first, then deploy the folder the build produced")
	}
	fallback := ""
	if d.Spec.Static != nil {
		fallback = d.Spec.Static.Fallback
	}
	if fallback != "" && fallback != "index.html" {
		// Checked like index.html and for the same reason: routed to a page
		// that is not there, every unknown path would answer with an error.
		code, _, err := e.rt.Exec(ctx, proxyID, []string{"test", "-f", check + "/" + fallback}, staticExecTimeout)
		if err != nil {
			return fmt.Errorf("could not look for %s in the proxy: %w", fallback, err)
		}
		if code != 0 {
			return fmt.Errorf("the folder has no %s, which static.fallback names: name a file the build produces", fallback)
		}
	}
	if !present {
		if err := e.proxyExec(ctx, proxyID, "mv", r.staticPart, dir); err != nil {
			return err
		}
		r.staticPart = ""
	}
	e.step(ctx, d, "Found index.html")
	if fallback != "" && fallback != "index.html" {
		e.step(ctx, d, "Found %s, the fallback page", fallback)
	}

	if err := r.reach(ctx, api.StatusHealthy); err != nil {
		return err
	}
	if !CanTransition(d.Status, api.StatusActive) {
		return fmt.Errorf("illegal state transition %s → %s", d.Status, api.StatusActive)
	}
	// From here to the commit the rollout says what the domain serves; the
	// database still names the previous deployment.
	e.routeVia(d.Application, routeOverride{hosts: hostnamesOf(d.Spec), staticRoot: dir})
	if err := e.SyncProxy(ctx); err != nil {
		return fmt.Errorf("could not route %s to the files: %w", d.Spec.Domain, err)
	}
	r.announceRouting(ctx, "the uploaded files")

	previous, err := e.store.ActivateDeployment(ctx, d.ID, time.Now())
	if err != nil {
		return err
	}
	d.Status = api.StatusActive
	e.clearRouteOverride(d.Application)
	e.event(ctx, d, api.LevelInfo, api.EventState, string(api.StatusActive))

	sweepCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), cleanupTimeout)
	defer cancel()
	// The previous version may have been a container application.
	e.retireOthers(sweepCtx, d, previous)
	if candidates, err := e.deployedImages(sweepCtx, d.Application); err == nil {
		if n := e.pruneImages(sweepCtx, candidates); n > 0 {
			e.step(sweepCtx, d, "Removed %s of older versions", plural(n, "image"))
		}
	}
	e.retireStaticDirs(sweepCtx, d, proxyID)
	e.step(sweepCtx, d, "Deployment successful")
	e.notifySucceeded(sweepCtx, d, previous)
	return nil
}

// retireStaticDirs removes the application's folders that no kept deployment
// serves: everything but the active deployment's and its most recent
// predecessor's, which is the rollback target, and whatever a crash left
// half-extracted.
//
// d may be a deployment that runs containers: the application was a folder
// and is one no longer. The folder it replaced is still the rollback target
// and stays; one container deployment later nothing can be rolled back to a
// folder without naming it, and the last of them goes.
func (e *Engine) retireStaticDirs(ctx context.Context, d *store.Deployment, proxyID string) {
	keep := map[string]bool{}
	if d.StaticDigest != "" {
		keep[strings.TrimPrefix(d.StaticDigest, "sha256:")] = true
	}
	superseded, err := e.store.ListDeployments(ctx, store.DeploymentFilter{Application: d.Application, Statuses: []api.DeploymentStatus{api.StatusSuperseded}})
	if err != nil {
		e.event(ctx, d, api.LevelWarn, api.EventStep, fmt.Sprintf("Could not list older versions: %v", err))
		return
	}
	for _, s := range superseded {
		if s.StaticDigest != "" && s.StaticDigest != d.StaticDigest {
			keep[strings.TrimPrefix(s.StaticDigest, "sha256:")] = true
			break
		}
		if d.StaticDigest == "" {
			break // its predecessor ran containers too
		}
	}

	code, out, err := e.rt.Exec(ctx, proxyID, []string{"ls", "-1", path.Join(staticRoot, d.Application)}, staticExecTimeout)
	if err != nil || code != 0 {
		e.event(ctx, d, api.LevelWarn, api.EventStep, fmt.Sprintf("Could not list the folders of older versions: %v", firstOf(err, errors.New(strings.TrimSpace(out)))))
		return
	}
	removed := 0
	for _, name := range strings.Split(out, "\n") {
		name = strings.TrimSpace(name)
		digest, half := strings.CutSuffix(name, ".part")
		if !isHexDigest(digest) || (keep[digest] && !half) {
			continue // not a folder this agent made, or one it still serves
		}
		if err := e.proxyExec(ctx, proxyID, "rm", "-rf", path.Join(staticRoot, d.Application, name)); err != nil {
			e.event(ctx, d, api.LevelWarn, api.EventStep, fmt.Sprintf("Could not remove the folder of an older version: %v", err))
			continue
		}
		removed++
	}
	if removed > 0 {
		e.step(ctx, d, "Removed %s of older versions", plural(removed, "folder"))
	}
	if len(keep) == 0 {
		// Nothing of the application is left in the proxy but the directory
		// that held its folders.
		e.proxyExec(ctx, proxyID, "rm", "-rf", path.Join(staticRoot, d.Application))
	}
}

// retireStaticLeftovers is the sweep of a deployment that runs containers,
// for an application that was a folder before: without it the folders would
// stay in the proxy until the application is deleted. Most applications never
// were a folder, and the proxy need not be a container at all; then there is
// nothing to do and nothing to report.
func (e *Engine) retireStaticLeftovers(ctx context.Context, d *store.Deployment) {
	if e.opts.Proxy == nil {
		return
	}
	proxyID, err := e.rt.ProxyContainer(ctx)
	if err != nil {
		return
	}
	if present, err := e.proxyDirExists(ctx, proxyID, path.Join(staticRoot, d.Application)); err != nil || !present {
		return
	}
	e.retireStaticDirs(ctx, d, proxyID)
}

// removeStaticFiles is Delete's part for a static application: its folders in
// the proxy, and its upload. The proxy is asked only when the history says
// the application ever had a folder there; a container application's delete
// must not depend on the proxy being a container.
func (e *Engine) removeStaticFiles(ctx context.Context, name string) {
	if e.opts.UploadDir != "" {
		os.RemoveAll(filepath.Join(e.opts.UploadDir, name))
	}
	history, err := e.store.ListDeployments(ctx, store.DeploymentFilter{Application: name})
	if err != nil {
		return
	}
	served := false
	for _, d := range history {
		served = served || d.StaticDigest != ""
	}
	if !served {
		return
	}
	proxyID, err := e.rt.ProxyContainer(ctx)
	if err == nil {
		err = e.proxyExec(ctx, proxyID, "rm", "-rf", path.Join(staticRoot, name))
	}
	if err != nil {
		e.log.Warn("could not remove the application's files from the proxy", "app", name, "error", err)
	}
}

// proxyExec runs one of the few commands the rollout needs inside the proxy's
// container — Caddy's image has a shell-less busybox for them — and turns a
// non-zero exit into an error.
func (e *Engine) proxyExec(ctx context.Context, proxyID string, cmd ...string) error {
	code, out, err := e.rt.Exec(ctx, proxyID, cmd, staticExecTimeout)
	if err != nil {
		return fmt.Errorf("%s in the proxy: %w", cmd[0], err)
	}
	if code != 0 {
		return fmt.Errorf("%s in the proxy exited with code %d: %s", cmd[0], code, strings.TrimSpace(out))
	}
	return nil
}

func (e *Engine) proxyDirExists(ctx context.Context, proxyID, dir string) (bool, error) {
	code, _, err := e.rt.Exec(ctx, proxyID, []string{"test", "-d", dir}, staticExecTimeout)
	if err != nil {
		return false, fmt.Errorf("test in the proxy: %w", err)
	}
	return code == 0, nil
}

func isHexDigest(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}

func firstOf(errs ...error) error {
	for _, err := range errs {
		if err != nil {
			return err
		}
	}
	return nil
}
