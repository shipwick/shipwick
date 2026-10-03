package api

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strconv"
	"time"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/backupfile"
)

const (
	defaultBackupLimit = 50
	maxBackupLimit     = 500
	// latestBackup stands for the application's latest successful backup
	// where a verification names one.
	latestBackup = "latest"
)

// backupRoutes registers scheduled backups and the agent's own state.
func (s *Server) backupRoutes(routes routeTable) {
	routes.read("GET /api/v1/applications/{name}/backups", s.withName(s.handleListBackups))
	routes.read("GET /api/v1/applications/{name}/backups/{id}", s.withName(s.handleGetBackup))
	// Taking a backup may run a command in the replica and stop the
	// application, and a verification starts a container from its image:
	// both are what deploying is. Reading the data out, replacing it and
	// removing a backup take admin, like `backup` and `restore` do.
	routes.deploy("POST /api/v1/applications/{name}/backups", s.withName(s.handleStartBackup))
	routes.deploy("POST /api/v1/applications/{name}/backups/{id}/verify", s.withName(s.handleVerifyBackup))
	routes.admin("POST /api/v1/applications/{name}/backups/{id}/restore", s.withName(s.handleRestoreBackup))
	routes.admin("GET /api/v1/applications/{name}/backups/{id}/volumes/{volume}/archive", s.withName(s.handleBackupArchive))
	routes.admin("DELETE /api/v1/applications/{name}/backups/{id}", s.withName(s.handleDeleteBackup))

	// The agent's state holds every secret; nothing about it is for less
	// than admin.
	routes.admin("GET /api/v1/server/backups", s.handleListStateBackups)
	routes.admin("GET /api/v1/server/backups/{id}", s.handleGetStateBackup)
	routes.admin("POST /api/v1/server/backups", s.handleStartStateBackup)
}

func (s *Server) handleListBackups(w http.ResponseWriter, r *http.Request, name string) {
	limit, ok := intParam(w, r, "limit", defaultBackupLimit, 1, maxBackupLimit)
	if !ok {
		return
	}
	runs, err := s.engine.Backups(r.Context(), name, limit)
	if err != nil {
		s.writeBackupError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) handleGetBackup(w http.ResponseWriter, r *http.Request, name string) {
	id, ok := backupID(w, r, false)
	if !ok {
		return
	}
	run, err := s.engine.BackupRun(r.Context(), name, id)
	if err != nil {
		s.writeBackupError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleStartBackup(w http.ResponseWriter, r *http.Request, name string) {
	run, err := s.engine.StartBackup(r.Context(), name)
	if err != nil {
		s.writeBackupError(w, r, err)
		return
	}
	acceptBackup(w, name, run)
}

func (s *Server) handleVerifyBackup(w http.ResponseWriter, r *http.Request, name string) {
	id, ok := backupID(w, r, true)
	if !ok {
		return
	}
	run, err := s.engine.VerifyBackup(r.Context(), name, id)
	if err != nil {
		s.writeBackupError(w, r, err)
		return
	}
	acceptBackup(w, name, run)
}

func (s *Server) handleRestoreBackup(w http.ResponseWriter, r *http.Request, name string) {
	id, ok := backupID(w, r, false)
	if !ok {
		return
	}
	run, err := s.engine.RestoreBackup(r.Context(), name, id)
	if err != nil {
		s.writeBackupError(w, r, err)
		return
	}
	acceptBackup(w, name, run)
}

func (s *Server) handleDeleteBackup(w http.ResponseWriter, r *http.Request, name string) {
	id, ok := backupID(w, r, false)
	if !ok {
		return
	}
	if err := s.engine.DeleteBackup(r.Context(), name, id); err != nil {
		s.writeBackupError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}

// handleBackupArchive streams one volume of a backup as the tar archive it
// was taken as, decrypted. As with handleBackup, an error found before the
// first byte is a JSON error; after that the only signal left is a cut
// connection.
func (s *Server) handleBackupArchive(w http.ResponseWriter, r *http.Request, name string) {
	id, ok := backupID(w, r, false)
	if !ok {
		return
	}
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

	archive, size, err := s.engine.OpenBackupArchive(ctx, name, id, volume)
	if err != nil {
		s.writeBackupError(w, r, err)
		return
	}
	defer archive.Close()
	// The first chunk is read before the answer starts: a passphrase that
	// does not match shows there, while it can still be said in words.
	buffered := bufio.NewReaderSize(archive, 256<<10)
	if _, err := buffered.Peek(1); err != nil && !errors.Is(err, io.EOF) {
		s.writeBackupError(w, r, err)
		return
	}

	rc := http.NewResponseController(w)
	w.Header().Set("Content-Type", "application/x-tar")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="%s-%s-backup-%d.tar"`, name, volume, id))
	// The length is known, and promised: a stream that ends early — a chunk
	// that does not decrypt — then ends the connection instead of looking
	// like a shorter archive.
	w.Header().Set("Content-Length", strconv.FormatInt(size, 10))
	w.WriteHeader(http.StatusOK)

	buf := make([]byte, 256<<10)
	for {
		n, rerr := buffered.Read(buf)
		if n > 0 {
			rc.SetWriteDeadline(time.Now().Add(archiveIdle))
			if _, err := w.Write(buf[:n]); err != nil {
				return
			}
			rc.Flush()
		}
		if rerr != nil {
			if !errors.Is(rerr, io.EOF) {
				s.log.Error("backup download interrupted", "app", name, "backup", id, "volume", volume, "error", rerr)
			}
			return
		}
	}
}

func (s *Server) handleListStateBackups(w http.ResponseWriter, r *http.Request) {
	limit, ok := intParam(w, r, "limit", defaultBackupLimit, 1, maxBackupLimit)
	if !ok {
		return
	}
	runs, err := s.engine.StateBackups(r.Context(), limit)
	if err != nil {
		s.writeBackupError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) handleGetStateBackup(w http.ResponseWriter, r *http.Request) {
	id, ok := backupID(w, r, false)
	if !ok {
		return
	}
	run, err := s.engine.StateBackup(r.Context(), id)
	if err != nil {
		s.writeBackupError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

func (s *Server) handleStartStateBackup(w http.ResponseWriter, r *http.Request) {
	run, err := s.engine.StartStateBackup(r.Context(), api.BackupTriggerManual)
	if err != nil {
		s.writeBackupError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/server/backups/"+strconv.FormatInt(run.ID, 10))
	writeJSON(w, http.StatusAccepted, run)
}

// acceptBackup answers 202: the work goes on in the background; poll the
// Location until completed_at is set, or until activity is empty again.
func acceptBackup(w http.ResponseWriter, name string, run api.BackupRun) {
	w.Header().Set("Location", "/api/v1/applications/"+name+"/backups/"+strconv.FormatInt(run.ID, 10))
	writeJSON(w, http.StatusAccepted, run)
}

// backupID reads the {id} path segment. Where allowLatest is set, "latest"
// is accepted and comes back as 0.
func backupID(w http.ResponseWriter, r *http.Request, allowLatest bool) (int64, bool) {
	raw := r.PathValue("id")
	if allowLatest && raw == latestBackup {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "backup id must be a positive number", nil)
		return 0, false
	}
	return id, true
}

// writeBackupError maps what only backups can fail with, and leaves the rest
// to writeEngineError.
func (s *Server) writeBackupError(w http.ResponseWriter, r *http.Request, err error) {
	switch {
	case errors.Is(err, deploy.ErrBackupBusy):
		writeError(w, http.StatusConflict, api.CodeBackupBusy, err.Error(), nil)
	case errors.Is(err, deploy.ErrBackupNotUsable):
		writeError(w, http.StatusConflict, api.CodeBackupNotUsable, err.Error(), nil)
	case errors.Is(err, deploy.ErrNoVolumes):
		writeError(w, http.StatusConflict, api.CodeNoVolumes, err.Error(), nil)
	case errors.Is(err, deploy.ErrNoBackup):
		writeError(w, http.StatusNotFound, api.CodeNotFound, err.Error(), nil)
	case errors.Is(err, deploy.ErrStateNotEncrypted), errors.Is(err, backup.ErrNoPassphrase):
		writeError(w, http.StatusConflict, api.CodeBackupsNotEncrypted, err.Error(), nil)
	case errors.Is(err, backupfile.ErrPassphrase), errors.Is(err, backupfile.ErrCorrupt):
		// The backup exists and cannot be read: the caller's request was
		// fine, and so is the agent.
		writeError(w, http.StatusConflict, api.CodeBackupNotUsable, err.Error(), nil)
	case errors.Is(err, deploy.ErrBackupsDisabled):
		writeError(w, http.StatusConflict, api.CodeBackupNotUsable, err.Error(), nil)
	default:
		s.writeEngineError(w, r, err)
	}
}
