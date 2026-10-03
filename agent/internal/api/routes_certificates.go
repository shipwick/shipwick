package api

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// maxCertificateBody bounds a PUT /certificates body: the chain and the key
// at their largest, JSON escaping and the envelope around them.
const maxCertificateBody = 4*2*api.MaxCertificatePEMBytes + 1024

// certificateRoutes registers certificates supplied by the operator. Anyone
// who may read sees which hostnames have one and when it expires; supplying
// and removing them is admin's: a key is being handed over, and every
// application's hostnames are affected.
func (s *Server) certificateRoutes(routes routeTable) {
	routes.read("GET /api/v1/certificates", s.handleListCertificates)
	routes.admin("PUT /api/v1/certificates/{hostname}", s.withHostname(s.handleSetCertificate))
	routes.admin("DELETE /api/v1/certificates/{hostname}", s.withHostname(s.handleDeleteCertificate))
}

// withHostname validates the {hostname} path segment the way a deploy.yaml's
// domain is validated: a plain hostname, or a wildcard.
func (s *Server) withHostname(next func(http.ResponseWriter, *http.Request, string)) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		hostname := strings.ToLower(r.PathValue("hostname"))
		if err := spec.ValidateHostname(hostname); err != nil {
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "hostname: "+err.Error(), nil)
			return
		}
		next(w, r, hostname)
	}
}

func (s *Server) handleListCertificates(w http.ResponseWriter, r *http.Request) {
	certificates, err := s.engine.Certificates(r.Context())
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, certificates)
}

// handleSetCertificate stores a certificate and its key under a hostname,
// replacing what is there. The key is in the body and nowhere else
// afterwards: not in the log, not in the response, not in any error.
func (s *Server) handleSetCertificate(w http.ResponseWriter, r *http.Request, hostname string) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, maxCertificateBody))
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "request body is too large or unreadable", nil)
		return
	}
	var req api.SetCertificateRequest
	dec := json.NewDecoder(bytes.NewReader(body))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, `invalid JSON body: expected {"certificate": "<PEM>", "key": "<PEM>"}`, nil)
		return
	}
	for _, part := range []struct{ name, value string }{{"certificate", req.Certificate}, {"key", req.Key}} {
		switch {
		case strings.TrimSpace(part.value) == "":
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, part.name+" is required: PEM text", nil)
			return
		case len(part.value) > api.MaxCertificatePEMBytes:
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest,
				fmt.Sprintf("%s is larger than %d KB", part.name, api.MaxCertificatePEMBytes/1024), nil)
			return
		}
	}
	stored, err := s.engine.SetCertificate(r.Context(), hostname, req.Certificate, req.Key)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, stored)
}

func (s *Server) handleDeleteCertificate(w http.ResponseWriter, r *http.Request, hostname string) {
	if err := s.engine.DeleteCertificate(r.Context(), hostname); err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	w.WriteHeader(http.StatusNoContent)
}
