package docker

import (
	"archive/tar"
	"context"
	"errors"
	"fmt"
	"io"
	"path"
	"strings"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/client"
)

// ExportPath streams a tar archive of a path inside the container — a
// volume's mount point, for backups. The container may be stopped: the
// archive endpoint reads the container's filesystem, not its process.
//
// Docker roots the archive at the path's own directory: exporting
// /var/lib/data yields "data/", "data/base/…". The daemon also accepts a
// trailing "/." to mean "the contents", but how it then names the entries is
// a detail of its rebasing that the API does not promise. The archive is
// rewritten here instead, so that a backup holds the volume's contents
// relative to the mount point whatever the daemon did, and ImportPath into
// the same mount point puts them back where they were.
func (r *Runtime) ExportPath(ctx context.Context, id, path string) (io.ReadCloser, error) {
	res, err := r.cli.CopyFromContainer(ctx, id, client.CopyFromContainerOptions{SourcePath: path})
	if err != nil {
		return nil, fmt.Errorf("export %s: %w", path, wrapNotFound(err))
	}
	if !res.Stat.Mode.IsDir() {
		res.Content.Close()
		return nil, fmt.Errorf("export %s: not a directory", path)
	}
	return rebaseArchive(res.Content, res.Stat.Name), nil
}

// ImportPath extracts a tar archive into a path inside the container. The
// entries are relative to that path, as ExportPath writes them; an archive
// with "./" entries, as `docker cp` makes them, works too.
func (r *Runtime) ImportPath(ctx context.Context, id, path string, archive io.Reader) error {
	_, err := r.cli.CopyToContainer(ctx, id, client.CopyToContainerOptions{DestinationPath: path, Content: archive})
	if err != nil {
		return fmt.Errorf("import into %s: %w", path, wrapNotFound(err))
	}
	return nil
}

// RemoveVolume removes an application's volume with everything in it. The
// daemon refuses while a container mounts it, which is right: a restore
// removes the containers first. A missing volume is not an error.
//
// It is not part of the engine's Runtime interface; the engine reaches it
// through an optional interface (see deploy/volumes.go).
func (r *Runtime) RemoveVolume(ctx context.Context, app, volume string) error {
	name := VolumeName(app, volume)
	_, err := r.cli.VolumeRemove(ctx, name, client.VolumeRemoveOptions{})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove volume %s: %w", name, err)
	}
	return nil
}

// rebaseArchive rewrites a tar stream whose entries all live under base so
// that they are relative to it: "base/x/y" becomes "x/y", and the entry for
// base itself is dropped. Hard links name other entries of the archive and
// move with them; symlinks are filesystem paths and are left alone. The
// rewrite streams: nothing is held beyond one tar header.
func rebaseArchive(src io.ReadCloser, base string) io.ReadCloser {
	pr, pw := io.Pipe()
	go func() {
		pw.CloseWithError(rebase(tar.NewReader(src), tar.NewWriter(pw), base))
	}()
	return &archiveStream{pr: pr, src: src}
}

func rebase(in *tar.Reader, out *tar.Writer, base string) error {
	prefix := base + "/"
	for {
		hdr, err := in.Next()
		if errors.Is(err, io.EOF) {
			return out.Close()
		}
		if err != nil {
			return fmt.Errorf("read archive: %w", err)
		}
		name := path.Clean(hdr.Name)
		if name == base {
			continue
		}
		rel, ok := strings.CutPrefix(name, prefix)
		if !ok {
			return fmt.Errorf("read archive: entry %q is outside %s", hdr.Name, base)
		}
		if hdr.Typeflag == tar.TypeDir {
			rel += "/"
		}
		hdr.Name = rel
		if hdr.Typeflag == tar.TypeLink {
			hdr.Linkname = strings.TrimPrefix(path.Clean(hdr.Linkname), prefix)
		}
		if err := out.WriteHeader(hdr); err != nil {
			return fmt.Errorf("write archive: %w", err)
		}
		if _, err := io.Copy(out, in); err != nil {
			return fmt.Errorf("copy %s: %w", rel, err)
		}
	}
}

// archiveStream is the rebased archive. Closing it also ends the daemon's
// response, which stops the rewriting goroutine.
type archiveStream struct {
	pr  *io.PipeReader
	src io.ReadCloser
}

func (s *archiveStream) Read(p []byte) (int, error) { return s.pr.Read(p) }

func (s *archiveStream) Close() error {
	s.pr.Close()
	return s.src.Close()
}
