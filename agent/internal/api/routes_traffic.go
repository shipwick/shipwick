package api

import (
	"net/http"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/pkg/api"
)

// trafficRoutes registers what the proxy saw of an application's traffic, and its certificates.
func (s *Server) trafficRoutes(routes routeTable) {
	routes.read("GET /api/v1/applications/{name}/traffic", s.withName(s.handleTraffic))
	routes.read("GET /api/v1/applications/{name}/requests", s.withName(s.handleRequests))
}

// handleTraffic answers traffic?since=1h|24h|7d. As with the metrics history,
// the bucket width is the server's choice per window.
func (s *Server) handleTraffic(w http.ResponseWriter, r *http.Request, name string) {
	since := r.URL.Query().Get("since")
	if since == "" {
		since = "1h"
	}
	window, step, ok := deploy.TrafficWindow(since)
	if !ok {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "since must be "+deploy.HistoryRanges, nil)
		return
	}
	traffic, err := s.engine.Traffic(r.Context(), name, window, step)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, traffic)
}

const (
	defaultRequestTail = 50
	// The agent remembers this many requests per application, and no more.
	maxRequestTail = 200
)

func (s *Server) handleRequests(w http.ResponseWriter, r *http.Request, name string) {
	tail, ok := intParam(w, r, "tail", defaultRequestTail, 1, maxRequestTail)
	if !ok {
		return
	}
	requests, err := s.engine.Requests(r.Context(), name, tail)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, requests)
}
