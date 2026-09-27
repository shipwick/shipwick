package api

import (
	"context"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const (
	// maxArchiveBytes bounds a restore upload.
	maxArchiveBytes = 10 << 30
	// archiveIdle is how long one read or write of an archive may stall
	// before the connection is given up. It replaces the server-wide
	// timeouts, which are sized for JSON, not for gigabytes.
	archiveIdle = 60 * time.Second
)

func (s *Server) handleListVolumes(w http.ResponseWriter, r *http.Request, name string) {
	volumes, err := s.engine.Volumes(r.Context(), name)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, volumes)
}

// handleBackup streams the volume as a tar archive: the body is the archive
// itself, not a JSON envelope. Errors found before the first byte are regular
// JSON errors; after that the only signal left is a cut connection.
func (s *Server) handleBackup(w http.ResponseWriter, r *http.Request, name string) {
	volume, ok := s.volumeName(w, r)
	if !ok {
		return
	}
	ctx, cancel := context.WithCancel(r.Context())
	defer cancel()
	go func() {
		select {
		case <-s.closing:
			cancel()
		case <-ctx.Done():
		}
	}()

	archive, err := s.engine.Backup(ctx, name, volume)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	defer archive.Close()

	rc := http.NewResponseController(w)
	stamp := time.Now().UTC().Format("20060102-150405")
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s-%s.tar"`, name, volume, stamp))
	w.WriteHeader(http.StatusOK)

	buf := make([]byte, 256<<10)
	for {
		n, rerr := archive.Read(buf)
		if n > 0 {
			rc.SetWriteDeadline(time.Now().Add(archiveIdle))
			if _, err := w.Write(buf[:n]); err != nil {
				return // the client went away; cancel() ends the export
			}
			rc.Flush()
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				s.log.Error("volume backup interrupted", "app", name, "volume", volume, "error", rerr)
			}
			return
		}
	}
}

// handleRestore takes a tar archive as the request body and replaces the
// volume's contents with it.
func (s *Server) handleRestore(w http.ResponseWriter, r *http.Request, name string) {
	volume, ok := s.volumeName(w, r)
	if !ok {
		return
	}
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/x-tar" {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "the body must be a tar archive sent as Content-Type: application/x-tar", nil)
		return
	}
	tooLarge := fmt.Sprintf("the archive exceeds %d GB", maxArchiveBytes>>30)
	// Refused before the volume is touched when the client says how much is
	// coming; a chunked upload that grows past the limit fails mid-restore.
	if r.ContentLength > maxArchiveBytes {
		writeError(w, http.StatusRequestEntityTooLarge, api.CodeInvalidRequest, tooLarge, nil)
		return
	}

	rc := http.NewResponseController(w)
	body := &deadlineReader{r: http.MaxBytesReader(w, r.Body, maxArchiveBytes), rc: rc}
	err := s.engine.Restore(r.Context(), name, volume, body)
	// The upload may have taken longer than the write deadline set when the
	// request arrived; the answer needs a fresh one.
	rc.SetWriteDeadline(time.Now().Add(archiveIdle))
	if err != nil {
		var maxErr *http.MaxBytesError
		if errors.As(body.err, &maxErr) {
			writeError(w, http.StatusRequestEntityTooLarge, api.CodeInvalidRequest, tooLarge, nil)
			return
		}
		s.writeEngineError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// volumeName validates the {volume} path segment: volume names follow the
// same rule as application names, and end up in Docker volume names.
func (s *Server) volumeName(w http.ResponseWriter, r *http.Request) (string, bool) {
	volume := r.PathValue("volume")
	if err := spec.ValidateName(volume); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "volume "+strings.TrimPrefix(err.Error(), "name "), nil)
		return "", false
	}
	return volume, true
}

// deadlineReader pushes the connection's read deadline ahead of every read,
// so that an upload is bounded by how long it stalls, not by its size. It
// keeps the last error, which the handler needs to tell "too large" from a
// broken upload.
type deadlineReader struct {
	r   io.Reader
	rc  *http.ResponseController
	err error
}

func (d *deadlineReader) Read(p []byte) (int, error) {
	d.rc.SetReadDeadline(time.Now().Add(archiveIdle))
	n, err := d.r.Read(p)
	if err != nil && !errors.Is(err, io.EOF) {
		d.err = err
	}
	return n, err
}

func (s *Server) handleListManagedVolumes(w http.ResponseWriter, r *http.Request) {
	volumes, err := s.engine.ManagedVolumes(r.Context())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, volumes)
}

// handleRemoveManagedVolume removes a volume by its Docker name. The name is
// taken apart and both halves validated before anything is looked up: they
// are an application name and a volume name, and end up in a Docker call.
func (s *Server) handleRemoveManagedVolume(w http.ResponseWriter, r *http.Request) {
	name := r.PathValue("name")
	app, volume, err := docker.ParseVolumeName(name)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	if err := spec.ValidateName(app); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "volume "+name+": application "+err.Error(), nil)
		return
	}
	if err := spec.ValidateName(volume); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "volume "+name+": volume "+strings.TrimPrefix(err.Error(), "name "), nil)
		return
	}
	if err := s.engine.RemoveManagedVolume(r.Context(), name); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
