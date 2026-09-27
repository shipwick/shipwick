package api

import (
	"net/http"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/pkg/api"
)

// handleMetricsHistory answers metrics/history?since=1h|24h|7d. The bucket
// width is the server's choice, per window, so that a series stays small
// enough to chart whatever the window.
func (s *Server) handleMetricsHistory(w http.ResponseWriter, r *http.Request, name string) {
	since := r.URL.Query().Get("since")
	if since == "" {
		since = "1h"
	}
	window, step, ok := deploy.HistoryWindow(since)
	if !ok {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "since must be "+deploy.HistoryRanges, nil)
		return
	}
	history, err := s.engine.MetricsHistory(r.Context(), name, window, step)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, history)
}
