package api

import (
	"errors"
	"fmt"
	"io"
	"net/http"

	"github.com/shipwick/shipwick/pkg/api"
)

// readBody reads a JSON body of at most limit bytes. One over the limit is
// answered 413, as every bounded body is; one that cannot be read, 400. It
// reports whether the caller may go on.
func readBody(w http.ResponseWriter, r *http.Request, limit int64) ([]byte, bool) {
	body, err := io.ReadAll(http.MaxBytesReader(w, r.Body, limit))
	if err == nil {
		return body, true
	}
	var tooLarge *http.MaxBytesError
	if errors.As(err, &tooLarge) {
		writeError(w, http.StatusRequestEntityTooLarge, api.CodeInvalidRequest, fmt.Sprintf("the body exceeds %d KB", limit>>10), nil)
		return nil, false
	}
	writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "request body is unreadable", nil)
	return nil, false
}
