package api

import (
	"bytes"
	"io"
	"net/http"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// documentRoutes are the endpoints that take a deploy.yaml and act on the
// application it names, with no name in their address: the dashboard deploys
// a document somebody pasted, and does not parse it to find out where to send
// it. Which application such a request is about is still decided before its
// handler runs (nameDocument), so that a caller limited to some applications
// is checked against it and the audit trail names it, exactly as for an
// endpoint under /applications/{name}.
var documentRoutes = map[string]bool{
	"POST /api/v1/applications": true,
	"POST /api/v1/validate":     true,
}

// configRoutes registers the document of an application and the endpoints
// that take one without a name in the address.
//
// The document shows more than GET /applications/{name} does — the text
// around a reference to a secret, which is masked there — and nothing that
// whoever may deploy the application cannot already read by running a command
// in it. So it takes that role, and a caller limited to some applications
// gets the documents of those.
func (s *Server) configRoutes(routes routeTable) {
	routes.deploy("GET /api/v1/applications/{name}/config", s.withName(s.handleConfig))
	routes.deploy("POST /api/v1/applications", s.handleDeployDocument)
	routes.deploy("POST /api/v1/validate", s.handleValidateDocument)
}

// nameDocument reads the name of the application out of the document a
// request carries and makes it the request's {name}, which is where the
// application check and the audit trail look. The body is put back as it
// was. A document whose name cannot be read — it is not YAML, it has none, it
// is not a name — leaves the request without one: its handler refuses the
// document for that, in the words of a validation, and a limited caller is
// refused before.
func nameDocument(r *http.Request) {
	head, _ := io.ReadAll(io.LimitReader(r.Body, spec.MaxConfigBytes+1))
	r.Body = struct {
		io.Reader
		io.Closer
	}{io.MultiReader(bytes.NewReader(head), r.Body), r.Body}
	if len(head) > spec.MaxConfigBytes {
		return
	}
	if name, ok := spec.DocumentName(head); ok {
		r.SetPathValue("name", name)
	}
}

// readDocument is readConfig for a document route. The name the application
// check saw is the name the document is deployed under: a document that
// parses to another is not let through.
func (s *Server) readDocument(w http.ResponseWriter, r *http.Request) (app spec.App, ok bool) {
	app, ok = s.parseConfig(w, r)
	if !ok {
		return app, false
	}
	if app.Name != r.PathValue("name") {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "the name of the application could not be read from the document; write it as name: my-api at the top", nil)
		return app, false
	}
	return app, true
}

// handleDeployDocument is handleDeploy for a document that says itself which
// application it is.
func (s *Server) handleDeployDocument(w http.ResponseWriter, r *http.Request) {
	app, ok := s.readDocument(w, r)
	if !ok {
		return
	}
	d, ok := s.startDeploy(w, r, app)
	if !ok {
		return
	}
	s.acceptDeployment(w, d)
}

// handleValidateDocument is handleValidate for such a document.
func (s *Server) handleValidateDocument(w http.ResponseWriter, r *http.Request) {
	app, ok := s.readDocument(w, r)
	if !ok {
		return
	}
	if err := s.engine.Validate(r.Context(), app); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.Validation{Valid: true})
}

// handleConfig answers the deploy.yaml that describes what the application
// runs: see deploy.Engine.Config. ?escape=true writes it for a file the CLI
// reads.
func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request, name string) {
	escape, err := boolParam(r, "escape")
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, err.Error(), nil)
		return
	}
	config, err := s.engine.Config(r.Context(), name, escape)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, config)
}
