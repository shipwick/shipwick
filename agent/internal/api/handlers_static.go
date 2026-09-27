package api

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// handleUploadStatic takes the folder of a static application as a tar
// archive and keeps it for the deployment that names its digest.
func (s *Server) handleUploadStatic(w http.ResponseWriter, r *http.Request, name string) {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/x-tar" {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "the body must be a tar archive of the folder sent as Content-Type: application/x-tar", nil)
		return
	}
	tooLarge := fmt.Sprintf("the folder exceeds %d MB", spec.MaxStaticBytes>>20)
	if r.ContentLength > spec.MaxStaticBytes {
		writeError(w, http.StatusRequestEntityTooLarge, api.CodeInvalidRequest, tooLarge, nil)
		return
	}

	rc := http.NewResponseController(w)
	body := &deadlineReader{r: http.MaxBytesReader(w, r.Body, spec.MaxStaticBytes), rc: rc}
	upload, err := s.engine.StoreStatic(r.Context(), name, body)
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
	writeJSON(w, http.StatusOK, upload)
}

// startDeploy hands a parsed deploy.yaml to the engine. A static application
// names the upload it serves in ?static=; no other application may. Errors are
// written here; ok says whether d is a deployment to answer with.
func (s *Server) startDeploy(w http.ResponseWriter, r *http.Request, app spec.App) (d store.Deployment, ok bool) {
	digest := r.URL.Query().Get("static")
	var err error
	switch {
	case app.Static == nil && digest != "":
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "static names an uploaded folder, and this deploy.yaml describes a container application", nil)
		return d, false
	case app.Static != nil && digest == "":
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest,
			"a static application is deployed with the digest of its uploaded folder: upload it with PUT …/static first and pass its digest as ?static=", nil)
		return d, false
	case app.Static != nil:
		d, err = s.engine.DeployStatic(r.Context(), app, digest)
	default:
		d, err = s.engine.Deploy(r.Context(), app)
	}
	if err != nil {
		s.writeEngineError(w, r, err)
		return d, false
	}
	return d, true
}
