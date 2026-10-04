package commands

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// A static application is a folder the proxy serves itself. `shipwick deploy`
// sends the folder first, as a tar archive the agent keeps under its digest,
// then the deploy.yaml naming that digest; the agent puts the files in front of
// the proxy and routes the domain to them. Nothing is built here: the folder
// is sent as it is, so run the site's build first.

// startDeployment starts the deployment of one config: for a static
// application, the folder goes first. plain is what the file said about its
// env values (placeholders.plainOf); a static application has none.
func (c *cli) startDeployment(ctx context.Context, cl *client.Client, file string, app spec.App, data []byte, plain []string) (api.Deployment, error) {
	if app.Static == nil {
		return cl.DeployWith(ctx, app.Name, data, plain)
	}
	return c.deployStatic(ctx, cl, file, app, data)
}

func (c *cli) deployStatic(ctx context.Context, cl *client.Client, file string, app spec.App, data []byte) (api.Deployment, error) {
	shown := app.Static.Dir + "/"
	dir := filepath.Join(filepath.Dir(file), filepath.FromSlash(app.Static.Dir))
	info, err := os.Stat(dir)
	switch {
	case errors.Is(err, fs.ErrNotExist):
		return api.Deployment{}, fmt.Errorf("%s names the folder %s, which does not exist\n\nBuild the site first, then deploy the folder the build produced", file, shown)
	case err != nil:
		return api.Deployment{}, err
	case !info.IsDir():
		return api.Deployment{}, fmt.Errorf("%s names %s as the folder to serve, but it is a file", file, app.Static.Dir)
	}
	if _, err := os.Stat(filepath.Join(dir, "index.html")); err != nil {
		return api.Deployment{}, fmt.Errorf("the folder %s has no index.html\n\nBuild the site first, then deploy the folder the build produced", shown)
	}
	if fallback := app.Static.Fallback; fallback != "" {
		// The agent looks again, at what actually arrived.
		if info, err := os.Stat(filepath.Join(dir, filepath.FromSlash(fallback))); err != nil || info.IsDir() {
			return api.Deployment{}, fmt.Errorf("%s names %s as static.fallback, and the folder %s has no such file\n\nName a file the build produces: for a single-page application that is index.html", file, fallback, shown)
		}
	}
	if err := c.askFirst(ctx, cl, app, data, "uploaded"); err != nil {
		return api.Deployment{}, err
	}

	// Written to a file, not memory: a folder may be hundreds of megabytes,
	// and the request needs its length up front.
	archive, err := os.CreateTemp("", "shipwick-static-*.tar")
	if err != nil {
		return api.Deployment{}, err
	}
	defer os.Remove(archive.Name())
	defer archive.Close()
	files, size, err := writeStaticArchive(dir, archive, func(skipped, why string) {
		c.ui.Warn("skipped %s: %s", filepath.ToSlash(skipped), why)
	})
	if err != nil {
		return api.Deployment{}, err
	}
	total, err := archive.Seek(0, io.SeekCurrent)
	if err != nil {
		return api.Deployment{}, err
	}
	if _, err := archive.Seek(0, io.SeekStart); err != nil {
		return api.Deployment{}, err
	}

	c.ui.Progress("Uploading %s (%s)", shown, spec.FormatMemory(size))
	body := &uploadProgress{r: archive, total: total, show: func(pct int) {
		c.ui.Progress("Uploading %s (%s): %d%%", shown, spec.FormatMemory(size), pct)
	}}
	upload, err := cl.UploadStatic(ctx, app.Name, body, total)
	if err != nil {
		c.ui.Done()
		return api.Deployment{}, err
	}
	c.ui.Success("Uploaded %s: %s, %s", shown, plural(upload.Files, "file"), spec.FormatMemory(upload.SizeBytes))
	if upload.Files != files {
		// The agent's count is what will be served; a difference means the
		// folder changed under the upload, which is worth a line.
		c.ui.Warn("the folder had %d files when it was read and the agent counted %d", files, upload.Files)
	}
	return cl.DeployStatic(ctx, app.Name, data, upload.Digest)
}

// writeStaticArchive writes the folder to w as a tar archive of files and
// directories, paths relative to the folder with forward slashes, in a fixed
// order and with fixed ownership, modes and times: the same files make the
// same archive, and the same digest, on any machine. It reports the files
// written and their sizes added up.
//
// A symbolic link to a file inside the folder is sent as that file; one that
// leads out of the folder is skipped and reported to skip — the served folder
// must not depend on what else is on the machine that built it.
//
// What describes or configures the site is not the site: `deploy.yaml` and
// `shipwick.yaml`, `.git` and `.env` files are left out wherever they are,
// since everything sent is served to anyone who asks for its path. Other
// dotfiles go in: `.well-known` is content.
func keptOutOfStatic(base string, isDir bool) bool {
	switch {
	case isDir:
		return base == ".git"
	case base == "deploy.yaml" || base == "shipwick.yaml":
		return true
	case base == ".env" || strings.HasPrefix(base, ".env."):
		return true
	}
	return false
}

func writeStaticArchive(root string, w io.Writer, skip func(path, why string)) (files int, size int64, err error) {
	rootReal, err := filepath.EvalSymlinks(root)
	if err != nil {
		return 0, 0, err
	}
	tw := tar.NewWriter(w)
	epoch := time.Unix(0, 0)
	err = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, err := filepath.Rel(root, p)
		if err != nil {
			return err
		}
		if rel == "." {
			return nil
		}
		name := filepath.ToSlash(rel)
		if keptOutOfStatic(d.Name(), d.IsDir()) {
			if d.IsDir() {
				return fs.SkipDir
			}
			return nil
		}

		info, err := d.Info()
		if err != nil {
			return err
		}
		source := p
		if info.Mode()&fs.ModeSymlink != 0 {
			target, err := filepath.EvalSymlinks(p)
			if err != nil {
				skip(rel, "broken symbolic link")
				return nil
			}
			if !within(rootReal, target) {
				skip(rel, "symbolic link out of the folder")
				return nil
			}
			if info, err = os.Stat(target); err != nil {
				return err
			}
			if info.IsDir() {
				skip(rel, "symbolic link to a directory")
				return nil
			}
			source = target
		}

		switch {
		case info.IsDir():
			return tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755, ModTime: epoch})
		case !info.Mode().IsRegular():
			skip(rel, "not a regular file")
			return nil
		}
		if size += info.Size(); size > spec.MaxStaticBytes {
			return fmt.Errorf("the folder is larger than %d MB, which is the most a static application may be", spec.MaxStaticBytes>>20)
		}
		if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name, Mode: 0o644, Size: info.Size(), ModTime: epoch}); err != nil {
			return err
		}
		f, err := os.Open(source)
		if err != nil {
			return err
		}
		defer f.Close()
		// The header promised a size; a file that grew or shrank meanwhile
		// would corrupt the archive, so exactly that many bytes are written.
		if _, err := io.CopyN(tw, f, info.Size()); err != nil {
			return fmt.Errorf("%s changed while it was being read: %w", name, err)
		}
		files++
		return nil
	})
	if err != nil {
		return 0, 0, err
	}
	return files, size, tw.Close()
}

// within reports whether target, a resolved path, is root or under it.
func within(root, target string) bool {
	rel, err := filepath.Rel(root, target)
	return err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// describeServing is the line after a successful deployment: how the
// application is served.
func describeServing(app api.ApplicationDetail) string {
	if app.Static {
		return "served by the proxy"
	}
	return fmt.Sprintf("%d/%d replicas healthy", app.Replicas.Healthy, app.Replicas.Desired)
}

// staticProgressLabels replace progressLabels for a static deployment, which
// pulls no image and starts no container.
var staticProgressLabels = map[api.DeploymentStatus]string{
	api.StatusBuilding:       "Checking the uploaded files",
	api.StatusStarting:       "Copying the files to the proxy",
	api.StatusHealthChecking: "Looking for index.html",
	api.StatusHealthy:        "Switching over",
	api.StatusActive:         "Removing older versions",
}

// progressLabel says what the agent is busy with in a state.
func progressLabel(status api.DeploymentStatus, static bool) (string, bool) {
	if static {
		label, ok := staticProgressLabels[status]
		return label, ok
	}
	label, ok := progressLabels[status]
	return label, ok
}

// describeStaticFiles is the status line of a static application: what the
// proxy serves.
func describeStaticFiles(files *api.StaticFiles) string {
	if files == nil {
		return "served by the proxy"
	}
	return fmt.Sprintf("%s, %s, served by the proxy", plural(files.Files, "file"), spec.FormatMemory(files.SizeBytes))
}
