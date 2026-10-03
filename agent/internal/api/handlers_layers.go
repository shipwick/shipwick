package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"regexp"

	"github.com/shipwick/shipwick/pkg/api"
)

// maxImageLayers bounds a question about an image's layers. Docker's own
// limit is lower; this one keeps the request small.
const maxImageLayers = 256

var diffID = regexp.MustCompile(`^sha256:[0-9a-f]{64}$`)

// handleMissingLayers answers which layers of an image about to be sent the
// server's Docker does not have, so that the client can leave the others out
// of the archive. It reads the daemon and changes nothing. The answer names
// only layers the request named: a client learns whether the server has
// content it already holds the digest of, and nothing about any other.
func (s *Server) handleMissingLayers(w http.ResponseWriter, r *http.Request, _ string) {
	var req api.MissingLayersRequest
	dec := json.NewDecoder(http.MaxBytesReader(w, r.Body, 64<<10))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&req); err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "invalid JSON body: "+err.Error(), nil)
		return
	}
	example := `{"layers": ["sha256:<64 hex characters>"]}`
	if len(req.Layers) == 0 || len(req.Layers) > maxImageLayers {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, fmt.Sprintf("layers must list 1 to %d diff IDs, base layer first, e.g. %s", maxImageLayers, example), nil)
		return
	}
	for _, layer := range req.Layers {
		if !diffID.MatchString(layer) {
			writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "every layer is a diff ID, e.g. "+example, nil)
			return
		}
	}

	missing, err := s.engine.MissingLayers(r.Context(), req.Layers)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, api.MissingLayers{Missing: missing})
}
