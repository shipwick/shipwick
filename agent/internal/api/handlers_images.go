package api

import (
	"errors"
	"fmt"
	"mime"
	"net/http"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// maxImageBytes bounds an image upload.
const maxImageBytes = 4 << 30

// handleLoadImage takes an image archive (the `docker save` format) as the
// request body and loads it into the server's Docker, for an application whose
// image is built where `shipwick deploy` runs.
func (s *Server) handleLoadImage(w http.ResponseWriter, r *http.Request, name string) {
	if ct, _, _ := mime.ParseMediaType(r.Header.Get("Content-Type")); ct != "application/x-tar" {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "the body must be an image archive sent as Content-Type: application/x-tar", nil)
		return
	}
	tooLarge := fmt.Sprintf("the image exceeds %d GB", maxImageBytes>>30)
	if r.ContentLength > maxImageBytes {
		writeError(w, http.StatusRequestEntityTooLarge, api.CodeInvalidRequest, tooLarge, nil)
		return
	}

	// Like a volume restore: bounded by how long the upload stalls, not by
	// how large it is.
	rc := http.NewResponseController(w)
	body := &deadlineReader{r: http.MaxBytesReader(w, r.Body, maxImageBytes), rc: rc}
	loaded, err := s.engine.LoadImage(r.Context(), name, body)
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
	writeJSON(w, http.StatusCreated, loaded)
}
