package api

import (
	"bufio"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"mime"
	"net/http"
	"strconv"
	"time"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/backupfile"
	"github.com/shipwick/shipwick/pkg/spec"
)

// exportErrorTrailer carries the reason an export stopped after its first
// byte had been sent. The archive is then left without its last chunk, so
// that it cannot be taken for a whole one; the trailer says why in words.
const exportErrorTrailer = "X-Shipwick-Export-Error"

// exportRoutes registers the export and import of a whole server. An export
// holds every secret the server has, and an import replaces applications and
// their data: nothing here is for less than admin, except looking at what a
// standby holds, which shows names and hostnames only.
func (s *Server) exportRoutes(routes routeTable) {
	routes.admin("POST /api/v1/export", s.handleExport)
	routes.admin("GET /api/v1/exports", s.handleListExports)
	routes.admin("GET /api/v1/exports/{id}", s.handleGetExport)
	routes.admin("POST /api/v1/exports", s.handleStartExport)
	routes.admin("POST /api/v1/import", s.handleImport)
	routes.admin("GET /api/v1/import", s.handleImportStatus)
	routes.read("GET /api/v1/standby", s.handleStandby)
	routes.admin("POST /api/v1/standby/pull", s.handleStandbyPull)
	routes.admin("POST /api/v1/standby/promote", s.handleStandbyPromote)
}

// handleExport streams an export of the server, encrypted with the
// passphrase of the request. Nothing of it is written on the server, and
// nothing leaves the agent in clear: the engine writes into the encryption.
func (s *Server) handleExport(w http.ResponseWriter, r *http.Request) {
	var req api.ExportRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		// The body holds a passphrase: the error says nothing of its content.
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, `the body must be JSON: {"passphrase": "…"}`, nil)
		return
	}
	if len(req.Passphrase) < api.MinPassphraseLength {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest,
			fmt.Sprintf("passphrase must be at least %d characters long: it is all that protects every secret of the server", api.MinPassphraseLength), nil)
		return
	}
	for _, name := range req.Applications {
		if err := spec.ValidateName(name); err != nil {
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "applications: "+err.Error(), nil)
			return
		}
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

	// The engine writes clear text into a pipe that only this function
	// reads, and what is read goes through the encryption before it reaches
	// the connection. The first bytes are waited for before the answer
	// starts: what fails until then can still be said as an error.
	pr, pw := io.Pipe()
	defer pr.Close()
	go func() {
		pw.CloseWithError(s.engine.Export(ctx, pw, req.Applications))
	}()
	plain := bufio.NewReaderSize(pr, 256<<10)
	if _, err := plain.Peek(1); err != nil {
		s.writeTransferError(w, r, err)
		return
	}

	rc := http.NewResponseController(w)
	out := &deadlineWriter{w: w, rc: rc}
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="shipwick-export-%s.swexport"`, s.now().UTC().Format("20060102-150405")))
	w.Header().Set("Trailer", exportErrorTrailer)
	w.WriteHeader(http.StatusOK)

	sealed, err := backupfile.NewWriter(out, req.Passphrase)
	if err == nil {
		if _, err = io.Copy(sealed, plain); err == nil {
			err = sealed.Close()
		}
	}
	if err != nil {
		// Not closed: without its last chunk the file does not decrypt, and
		// nobody mistakes it for an export.
		s.log.Error("export interrupted", "error", err)
		w.Header().Set(exportErrorTrailer, err.Error())
	}
}

// deadlineWriter pushes the connection's write deadline ahead of every write
// and flushes after it, like the archive downloads do.
type deadlineWriter struct {
	w  http.ResponseWriter
	rc *http.ResponseController
}

func (d *deadlineWriter) Write(p []byte) (int, error) {
	d.rc.SetWriteDeadline(time.Now().Add(archiveIdle))
	n, err := d.w.Write(p)
	if err == nil {
		d.rc.Flush()
	}
	return n, err
}

func (s *Server) handleListExports(w http.ResponseWriter, r *http.Request) {
	limit, ok := intParam(w, r, "limit", defaultBackupLimit, 1, maxBackupLimit)
	if !ok {
		return
	}
	runs, err := s.engine.Exports(r.Context(), limit)
	if err != nil {
		s.writeTransferError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, runs)
}

func (s *Server) handleGetExport(w http.ResponseWriter, r *http.Request) {
	id, ok := backupID(w, r, false)
	if !ok {
		return
	}
	run, err := s.engine.ExportRun(r.Context(), id)
	if err != nil {
		s.writeTransferError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, run)
}

// handleStartExport writes an export to where backups go, as the schedule
// does. 202: poll the Location until completed_at is set.
func (s *Server) handleStartExport(w http.ResponseWriter, r *http.Request) {
	run, err := s.engine.StartExport(r.Context(), api.BackupTriggerManual)
	if err != nil {
		s.writeTransferError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/exports/"+strconv.FormatInt(run.ID, 10))
	writeJSON(w, http.StatusAccepted, run)
}

// handleImport takes an export as the request body and imports it while it
// arrives: nothing of it is kept on the server, encrypted or not. The answer
// comes when every application in it has been dealt with; GET /import shows
// how far it is meanwhile.
func (s *Server) handleImport(w http.ResponseWriter, r *http.Request) {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/octet-stream" {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "the body must be an export sent as Content-Type: application/octet-stream", nil)
		return
	}
	stopped, err := boolParam(r, "stopped")
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	overwrite, err := boolParam(r, "overwrite")
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	passphrase, err := base64.StdEncoding.DecodeString(r.Header.Get(api.PassphraseHeader))
	if err != nil || len(passphrase) == 0 {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "the passphrase of the export is sent base64-encoded in the "+api.PassphraseHeader+" header", nil)
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

	// Bounded by how long the upload stalls while it is being read, not by
	// how long the import takes: between two reads there may be a deployment.
	rc := http.NewResponseController(w)
	body := &deadlineReader{r: r.Body, rc: rc}
	plain, err := backupfile.NewReader(body, string(passphrase))
	if err != nil {
		rc.SetWriteDeadline(time.Now().Add(archiveIdle))
		if errors.Is(err, backupfile.ErrNotEncrypted) {
			writeError(w, http.StatusBadRequest, api.CodeInvalidExport, "this is not an export: it does not start like a file shipwick export writes", nil)
			return
		}
		writeError(w, http.StatusBadRequest, api.CodeInvalidExport, err.Error(), nil)
		return
	}
	result, err := s.engine.Import(ctx, plain, deploy.ImportOptions{Stopped: stopped, Overwrite: overwrite, Source: "upload"})
	rc.SetWriteDeadline(time.Now().Add(archiveIdle))
	var invalid *deploy.InvalidExportError
	switch {
	case errors.Is(err, deploy.ErrImportInProgress), errors.Is(err, deploy.ErrShuttingDown), errors.As(err, &invalid):
		s.writeTransferError(w, r, err)
	default:
		// An import that began is answered with its record, whatever became
		// of it: which applications are in place is what the caller needs.
		writeJSON(w, http.StatusOK, result)
	}
}

func (s *Server) handleImportStatus(w http.ResponseWriter, r *http.Request) {
	im, ok := s.engine.ImportStatus()
	if !ok {
		writeError(w, http.StatusNotFound, api.CodeNotFound, "no import has run on this server since the agent started", nil)
		return
	}
	writeJSON(w, http.StatusOK, im)
}

func (s *Server) handleStandby(w http.ResponseWriter, r *http.Request) {
	standby, err := s.engine.Standby(r.Context())
	if err != nil {
		s.writeTransferError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, standby)
}

// handleStandbyPull imports the newest export in the bucket, stopped. 202:
// poll GET /import until completed_at is set.
func (s *Server) handleStandbyPull(w http.ResponseWriter, r *http.Request) {
	im, err := s.engine.StartStandbyPull(r.Context())
	if err != nil {
		s.writeTransferError(w, r, err)
		return
	}
	w.Header().Set("Location", "/api/v1/import")
	writeJSON(w, http.StatusAccepted, im)
}

// handleStandbyPromote starts what was imported stopped. It waits for each
// application to be ready, so the answer may take as long as their startup
// budgets together.
func (s *Server) handleStandbyPromote(w http.ResponseWriter, r *http.Request) {
	promotion, err := s.engine.Promote(r.Context())
	afterGracePeriod(w)
	if err != nil {
		s.writeTransferError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, promotion)
}

// writeTransferError maps what only exports and imports can fail with, and
// leaves the rest to the backups' mapping and to writeEngineError.
func (s *Server) writeTransferError(w http.ResponseWriter, r *http.Request, err error) {
	var invalid *deploy.InvalidExportError
	switch {
	case errors.Is(err, deploy.ErrImportInProgress):
		writeError(w, http.StatusConflict, api.CodeImportInProgress, err.Error(), nil)
	case errors.Is(err, deploy.ErrExportInProgress):
		writeError(w, http.StatusConflict, api.CodeExportInProgress, err.Error(), nil)
	case errors.As(err, &invalid):
		writeError(w, http.StatusBadRequest, api.CodeInvalidExport, invalid.Reason, nil)
	case errors.Is(err, deploy.ErrNoStandbySource):
		writeError(w, http.StatusConflict, api.CodeStandbyNotConfigured, err.Error(), nil)
	case errors.Is(err, deploy.ErrNoExport):
		writeError(w, http.StatusNotFound, api.CodeNotFound, err.Error(), nil)
	case errors.Is(err, deploy.ErrExportNotEncrypted):
		writeError(w, http.StatusConflict, api.CodeBackupsNotEncrypted, err.Error(), nil)
	default:
		s.writeBackupError(w, r, err)
	}
}
