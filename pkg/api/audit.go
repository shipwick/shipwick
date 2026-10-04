package api

import (
	"fmt"
	"regexp"
	"slices"
	"strings"
)

// Searching the audit trail, and taking it out of the server.

// AuditPage is the envelope of GET /audit: Response, and whether entries
// older than the last one match as well. More is absent from agents older
// than 0.7, which is why it is a pointer here: a client that finds none
// falls back to guessing from a full page.
type AuditPage struct {
	Data []AuditEntry `json:"data"`
	More *bool        `json:"more,omitempty"`
}

// MaxAuditActions is how many actions one query can name.
const MaxAuditActions = 20

// Formats of GET /audit/export.
const (
	AuditFormatCSV  = "csv"
	AuditFormatJSON = "json" // one entry per line
)

// AuditColumns are the columns of an export as CSV, in order: the fields of
// AuditEntry.
var AuditColumns = []string{"id", "at", "actor_kind", "actor", "address", "forwarded_for", "action", "application", "target", "outcome", "status", "code", "detail"}

// An action as the trail records it — "deploy", "token.create" — or the
// start of one up to its dot: "token." is every action on tokens.
var auditActionPattern = regexp.MustCompile(`^[a-z]{1,20}(\.[a-z]{1,20})?$|^[a-z]{1,20}\.$`)

// ValidateAuditAction checks one value of the action filter.
func ValidateAuditAction(action string) error {
	if !auditActionPattern.MatchString(action) {
		return fmt.Errorf("invalid action %q: use one as the trail shows it, or the start of a family with its dot, e.g. deploy, token.create or backup.", action)
	}
	return nil
}

// ValidateAuditOutcome checks one value of the outcome filter.
func ValidateAuditOutcome(outcome string) error {
	if !slices.Contains([]string{AuditOK, AuditRefused, AuditFailed}, outcome) {
		return fmt.Errorf("invalid outcome %q: use ok, refused or failed", outcome)
	}
	return nil
}

// ValidateActorKind checks the kind-of-actor filter.
func ValidateActorKind(kind string) error {
	if kind != ActorToken && kind != ActorUser {
		return fmt.Errorf("invalid kind of actor %q: use token or user", kind)
	}
	return nil
}

// SplitList reads the values of a filter that takes several: given more than
// once, separated by commas, or both. Each value comes back once.
func SplitList(values []string) []string {
	out := []string{}
	for _, v := range values {
		for _, part := range strings.Split(v, ",") {
			if part = strings.TrimSpace(part); part != "" && !slices.Contains(out, part) {
				out = append(out, part)
			}
		}
	}
	return out
}

// SpreadsheetSafe returns a value as it can stand in a cell of a CSV file
// that someone will open in a spreadsheet. Excel, LibreOffice and Google
// Sheets take a cell that begins with =, +, - or @ for a formula, and one
// that begins with a tab or a carriage return after dropping it; such a
// value gets an apostrophe in front, which they show as text. The audit
// trail holds names that callers chose — a token's, a person's, a path
// segment — so this is where a name stops being able to run anything.
func SpreadsheetSafe(value string) string {
	if value == "" {
		return value
	}
	switch value[0] {
	case '=', '+', '-', '@', '\t', '\r':
		return "'" + value
	}
	return value
}
