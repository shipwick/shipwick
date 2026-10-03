package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// readConfig reads the deploy.yaml document a request carries as its body,
// for the application the URL names. It is the first half of a deployment
// and all of a validation up to the engine, so the two cannot come to answer
// the same document differently. Errors are written here; ok says whether
// app is a document to go on with.
func (s *Server) readConfig(w http.ResponseWriter, r *http.Request, name string) (app spec.App, ok bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, spec.MaxConfigBytes))
	if err != nil {
		var tooLarge *http.MaxBytesError
		if errors.As(err, &tooLarge) {
			writeError(w, http.StatusRequestEntityTooLarge, api.CodeInvalidRequest,
				fmt.Sprintf("request body exceeds %d KB", spec.MaxConfigBytes/1024), nil)
			return app, false
		}
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "could not read request body", nil)
		return app, false
	}

	app, err = spec.Parse(body)
	if err != nil {
		var verr *spec.ValidationError
		if errors.As(err, &verr) {
			writeError(w, http.StatusBadRequest, api.CodeInvalidConfig, "invalid deploy.yaml", map[string]any{"fields": verr.Fields})
			return app, false
		}
		writeError(w, http.StatusBadRequest, api.CodeInvalidConfig, err.Error(), nil)
		return app, false
	}
	if app.Name != name {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest,
			fmt.Sprintf("the config describes application %q but the URL names %q", app.Name, name), nil)
		return app, false
	}
	return app, true
}

// handleValidate answers what a deployment of the document would be answered,
// without deploying it. The CLI asks before it builds an image or uploads a
// folder, which is why neither is expected here: `build` without an image
// passes, and a static application names no upload.
func (s *Server) handleValidate(w http.ResponseWriter, r *http.Request, name string) {
	app, ok := s.readConfig(w, r, name)
	if !ok {
		return
	}
	if err := s.engine.Validate(r.Context(), app); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.Validation{Valid: true})
}
