package api

import (
	"errors"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/pkg/api"
)

const (
	defaultArchiveLimit = 50
	maxArchiveLimit     = 500
	// maxArchiveTail is more lines than an entry holds.
	maxArchiveTail = 10000

	defaultSearchLimit = 200
	maxSearchLimit     = 1000
	maxSearchText      = 256
	maxSearchCursor    = 64
)

// logRoutes registers the log archive. What a container printed is as
// sensitive when it is kept as when it is followed: the role that reads the
// logs reads the archive, and a token limited to applications reads theirs.
func (s *Server) logRoutes(routes routeTable) {
	routes.read("GET /api/v1/applications/{name}/logs/archive", s.withName(s.handleLogArchive))
	routes.read("GET /api/v1/applications/{name}/logs/archive/{id}", s.withName(s.handleArchivedLogs))
	routes.read("GET /api/v1/applications/{name}/logs/search", s.withName(s.handleSearchLogs))
}

// handleLogArchive lists what is kept, newest first:
// logs/archive?kind=replica|run&deployment=ID&replica=N&run=ID&before=ID&limit=N.
func (s *Server) handleLogArchive(w http.ResponseWriter, r *http.Request, name string) {
	q := deploy.LogArchiveQuery{Kind: r.URL.Query().Get("kind")}
	if q.Kind != "" && q.Kind != api.LogKindReplica && q.Kind != api.LogKindRun {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, fmt.Sprintf("kind must be %s or %s", api.LogKindReplica, api.LogKindRun), nil)
		return
	}
	var ok bool
	if q.DeploymentID, q.Replica, q.RunID, ok = logSource(w, r); !ok {
		return
	}
	if q.Before, ok = idParam(w, r, "before"); !ok {
		return
	}
	if q.Limit, ok = intParam(w, r, "limit", defaultArchiveLimit, 1, maxArchiveLimit); !ok {
		return
	}
	entries, err := s.engine.LogArchive(r.Context(), name, q)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, entries)
}

// handleArchivedLogs returns one entry with its lines; ?tail=N keeps the
// last N.
func (s *Server) handleArchivedLogs(w http.ResponseWriter, r *http.Request, name string) {
	id, err := strconv.ParseInt(r.PathValue("id"), 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "archive id must be a positive number", nil)
		return
	}
	tail, ok := intParam(w, r, "tail", 0, 1, maxArchiveTail)
	if !ok {
		return
	}
	detail, err := s.engine.ArchivedLogs(r.Context(), name, id, tail)
	if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, detail)
}

// handleSearchLogs answers one page of
// logs/search?q=TEXT&since=TIME&until=TIME&deployment=ID&replica=N&run=ID&limit=N&cursor=NEXT.
// Without q every line in the range matches.
func (s *Server) handleSearchLogs(w http.ResponseWriter, r *http.Request, name string) {
	query := r.URL.Query()
	q := deploy.LogSearch{Text: query.Get("q"), Cursor: query.Get("cursor")}
	switch {
	case len(q.Text) > maxSearchText:
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, fmt.Sprintf("q must be at most %d bytes", maxSearchText), nil)
		return
	case strings.ContainsAny(q.Text, "\r\n\x00"):
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "q must be one line: a search looks inside lines", nil)
		return
	case len(q.Cursor) > maxSearchCursor:
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "cursor must be the next of an earlier answer", nil)
		return
	}
	var ok bool
	if q.Since, ok = timeParam(w, r, "since"); !ok {
		return
	}
	if q.Until, ok = timeParam(w, r, "until"); !ok {
		return
	}
	if !q.Since.IsZero() && !q.Until.IsZero() && q.Until.Before(q.Since) {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "until must not be before since", nil)
		return
	}
	if q.DeploymentID, q.Replica, q.RunID, ok = logSource(w, r); !ok {
		return
	}
	if q.Limit, ok = intParam(w, r, "limit", defaultSearchLimit, 1, maxSearchLimit); !ok {
		return
	}
	result, err := s.engine.SearchLogs(r.Context(), name, q)
	if errors.Is(err, deploy.ErrInvalidCursor) {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "cursor must be the next of an earlier answer", nil)
		return
	} else if err != nil {
		s.writeEngineError(w, r, err)
		return
	}
	writeJSON(w, http.StatusOK, result)
}

// logSource reads the parameters that name where output comes from.
func logSource(w http.ResponseWriter, r *http.Request) (deployment int64, replica int, run int64, ok bool) {
	if deployment, ok = idParam(w, r, "deployment"); !ok {
		return
	}
	if replica, ok = intParam(w, r, "replica", 0, 1, 1000); !ok {
		return
	}
	run, ok = idParam(w, r, "run")
	return
}

// idParam reads an optional id; zero when it is not given.
func idParam(w http.ResponseWriter, r *http.Request, name string) (int64, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return 0, true
	}
	id, err := strconv.ParseInt(raw, 10, 64)
	if err != nil || id < 1 {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, name+" must be a positive number", nil)
		return 0, false
	}
	return id, true
}

// timeParam reads an optional time in RFC 3339; the zero time when it is not
// given.
func timeParam(w http.ResponseWriter, r *http.Request, name string) (time.Time, bool) {
	raw := r.URL.Query().Get(name)
	if raw == "" {
		return time.Time{}, true
	}
	at, err := time.Parse(time.RFC3339Nano, raw)
	if err != nil {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, name+" must be a time in RFC 3339, e.g. "+time.Now().UTC().Format(time.RFC3339), nil)
		return time.Time{}, false
	}
	return at.UTC(), true
}
