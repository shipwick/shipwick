package api

import (
	"bufio"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// auditFilter reads what narrows the trail from the query: the application,
// the actor and its kind, how far back, the actions and the outcomes. A
// filter that takes several values takes them repeated or separated by
// commas. It answers the request itself when something is not a filter.
func (s *Server) auditFilter(w http.ResponseWriter, r *http.Request) (store.AuditFilter, bool) {
	q := r.URL.Query()
	invalid := func(message string) (store.AuditFilter, bool) {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, message, nil)
		return store.AuditFilter{}, false
	}
	filter := store.AuditFilter{Application: q.Get("application"), Actor: q.Get("actor"), ActorKind: q.Get("actor_kind"),
		Actions: api.SplitList(q["action"]), Outcomes: api.SplitList(q["outcome"])}
	if filter.Application != "" {
		if err := spec.ValidateName(filter.Application); err != nil {
			return invalid(err.Error())
		}
	}
	if len(filter.Actor) > 320 {
		return invalid("actor is the name of a token or the address of a person, e.g. actor=ci")
	}
	if filter.ActorKind != "" {
		if err := api.ValidateActorKind(filter.ActorKind); err != nil {
			return invalid("actor_kind: " + err.Error())
		}
	}
	if len(filter.Actions) > api.MaxAuditActions {
		return invalid(fmt.Sprintf("action: at most %d at once; the start of a family covers all of it, e.g. action=backup.", api.MaxAuditActions))
	}
	for _, action := range filter.Actions {
		if err := api.ValidateAuditAction(action); err != nil {
			return invalid(err.Error())
		}
	}
	for _, outcome := range filter.Outcomes {
		if err := api.ValidateAuditOutcome(outcome); err != nil {
			return invalid(err.Error())
		}
	}
	if raw := q.Get("since"); raw != "" {
		since, err := api.ParseSince(raw, s.now())
		if err != nil {
			return invalid("since: " + err.Error())
		}
		filter.Since = since
	}
	if raw := q.Get("before"); raw != "" {
		before, err := strconv.ParseInt(raw, 10, 64)
		if err != nil || before < 1 {
			return invalid("before must be the id of an audit entry")
		}
		filter.Before = before
	}
	return filter, true
}

// describeAuditFilter is the filter as the audit trail records it for an
// export: what was taken out, in the words of the query.
func describeAuditFilter(f store.AuditFilter) string {
	var parts []string
	if f.Application != "" {
		parts = append(parts, "application "+f.Application)
	}
	if f.Actor != "" {
		parts = append(parts, "actor "+printable(f.Actor))
	}
	if f.ActorKind != "" {
		parts = append(parts, "actor_kind "+f.ActorKind)
	}
	if len(f.Actions) > 0 {
		parts = append(parts, "action "+strings.Join(f.Actions, " "))
	}
	if len(f.Outcomes) > 0 {
		parts = append(parts, "outcome "+strings.Join(f.Outcomes, " "))
	}
	if !f.Since.IsZero() {
		parts = append(parts, "since "+f.Since.UTC().Format(time.RFC3339))
	}
	return strings.Join(parts, ", ")
}

// auditExportFlush is how many entries are written between two flushes.
const auditExportFlush = 500

// handleAuditExport streams every entry that matches, newest first, as CSV
// or as one JSON object per line. It is the trail leaving the server —
// every token's name, every person's, and what each did — so it is admin's,
// and it is itself recorded: who took it, in which format, narrowed how,
// and how many entries.
//
// Nothing is held but the page being written (see store.EachAuditEntry). A
// failure before the first entry is answered like any other; one after it
// is said in the trailer, as an export of the server says it, and recorded
// as failed.
func (s *Server) handleAuditExport(w http.ResponseWriter, r *http.Request) {
	q := r.URL.Query()
	format := q.Get("format")
	if format != api.AuditFormatCSV && format != api.AuditFormatJSON {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "format is required: csv, or json for one entry per line", nil)
		return
	}
	if q.Has("limit") || q.Has("before") {
		writeError(w, http.StatusBadRequest, api.CodeInvalidRequest, "an export is everything that matches, not a page: leave limit and before out, and narrow it with since, application, actor, action or outcome", nil)
		return
	}
	filter, ok := s.auditFilter(w, r)
	if !ok {
		return
	}
	auditDetail(r, format)
	if narrowed := describeAuditFilter(filter); narrowed != "" {
		auditDetail(r, narrowed)
	}

	out := bufio.NewWriterSize(&deadlineWriter{w: w, rc: http.NewResponseController(w)}, 64<<10)
	var (
		entries int
		write   func(api.AuditEntry) error
		table   *csv.Writer
	)
	start := func() error {
		extension, contentType := "csv", "text/csv; charset=utf-8"
		if format == api.AuditFormatJSON {
			extension, contentType = "ndjson", "application/x-ndjson"
		}
		w.Header().Set("Content-Type", contentType)
		w.Header().Set("Content-Disposition", fmt.Sprintf(`attachment; filename="shipwick-audit-%s.%s"`, s.now().UTC().Format("20060102-150405"), extension))
		w.Header().Set("Trailer", exportErrorTrailer)
		w.WriteHeader(http.StatusOK)
		if format == api.AuditFormatJSON {
			lines := json.NewEncoder(out)
			write = func(e api.AuditEntry) error { return lines.Encode(e) }
			return nil
		}
		table = csv.NewWriter(out)
		write = func(e api.AuditEntry) error { return table.Write(auditRecord(e)) }
		return table.Write(api.AuditColumns)
	}
	flush := func() error {
		if table != nil {
			table.Flush()
			if err := table.Error(); err != nil {
				return err
			}
		}
		return out.Flush()
	}

	err := s.store.EachAuditEntry(r.Context(), filter, func(e api.AuditEntry) error {
		if write == nil {
			if err := start(); err != nil {
				return err
			}
		}
		if err := write(e); err != nil {
			return err
		}
		if entries++; entries%auditExportFlush == 0 {
			return flush()
		}
		return nil
	})
	switch {
	case err != nil && write == nil:
		s.writeEngineError(w, r, err)
		return
	case err == nil && write == nil:
		// Nothing matches: a file with its header and no rows, or no lines.
		err = start()
	}
	if err == nil {
		err = flush()
	}
	auditDetail(r, strconv.Itoa(entries)+" entries")
	if err != nil {
		s.log.Error("audit export interrupted", "error", err)
		w.Header().Set(exportErrorTrailer, "the export stopped after "+strconv.Itoa(entries)+" entries: it is not complete")
		return
	}
	s.log.Info("audit trail exported", "format", format, "entries", entries, "by", principalFrom(r.Context()).Name)
}

// auditRecord is an entry as a row of the CSV export, in the order of
// api.AuditColumns. Every cell that holds text goes through
// api.SpreadsheetSafe: the trail keeps names that callers chose.
func auditRecord(e api.AuditEntry) []string {
	safe := api.SpreadsheetSafe
	return []string{
		strconv.FormatInt(e.ID, 10),
		e.At.UTC().Format(time.RFC3339),
		safe(e.Actor.Kind), safe(e.Actor.Name), safe(e.Address), safe(e.ForwardedFor),
		safe(e.Action), safe(e.Application), safe(e.Target), safe(e.Outcome),
		strconv.Itoa(e.Status), safe(e.Code), safe(e.Detail),
	}
}
