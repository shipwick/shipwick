package api

import (
	"errors"
	"fmt"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/spec"
)

// Limited reports whether the caller may change only the applications it
// names. Admin is never limited, whatever the list says.
func (t TokenIdentity) Limited() bool {
	return len(t.Applications) > 0 && t.Role != RoleAdmin
}

// Allows reports whether the caller may do to the application what its role
// permits beyond reading. Reading is never limited.
func (t TokenIdentity) Allows(application string) bool {
	return !t.Limited() || slices.Contains(t.Applications, application)
}

// ValidateTokenApplications checks the applications a token is to be limited
// to and returns them sorted, each once. Only the deploy role can be limited:
// read changes nothing, and admin acts on the server as a whole.
func ValidateTokenApplications(role Role, applications []string) ([]string, error) {
	if len(applications) == 0 {
		return []string{}, nil
	}
	if role != RoleDeploy {
		return nil, fmt.Errorf("only a deploy token can be limited to applications: a read token changes nothing, and admin is for the whole server")
	}
	out := make([]string, 0, len(applications))
	for _, name := range applications {
		if err := spec.ValidateName(name); err != nil {
			return nil, fmt.Errorf("applications: %w", err)
		}
		if !slices.Contains(out, name) {
			out = append(out, name)
		}
	}
	if len(out) > MaxTokenApplications {
		return nil, fmt.Errorf("a token can be limited to at most %d applications", MaxTokenApplications)
	}
	slices.Sort(out)
	return out, nil
}

// maxSpan bounds "90d" and "12h": ten years is longer than any token should
// live, and short enough that the arithmetic cannot overflow.
const maxSpan = 3650 * 24 * time.Hour

// parseSpan reads a whole number of days or hours: "90d", "12h".
func parseSpan(s string) (time.Duration, bool) {
	if len(s) < 2 {
		return 0, false
	}
	unit := time.Hour
	switch s[len(s)-1] {
	case 'd':
		unit = 24 * time.Hour
	case 'h':
	default:
		return 0, false
	}
	n, err := strconv.Atoi(s[:len(s)-1])
	if err != nil || n < 1 || strings.HasPrefix(s, "+") || time.Duration(n) > maxSpan/unit {
		return 0, false
	}
	return time.Duration(n) * unit, true
}

const dateLayout = "2006-01-02"

// ParseExpiry reads when a token is to expire: in a number of days or hours
// from now ("90d", "12h"), on a date ("2027-01-31": the token works through
// that day, UTC), or at a time in RFC 3339.
func ParseExpiry(s string, now time.Time) (time.Time, error) {
	var at time.Time
	if span, ok := parseSpan(s); ok {
		at = now.Add(span)
	} else if day, err := time.Parse(dateLayout, s); err == nil {
		at = day.Add(24 * time.Hour)
	} else if at, err = time.Parse(time.RFC3339, s); err != nil {
		return time.Time{}, fmt.Errorf("invalid expiry %q: use days or hours from now, or a date, e.g. 90d, 12h or %s", s, now.AddDate(0, 3, 0).UTC().Format(dateLayout))
	}
	if !at.After(now) {
		return time.Time{}, errors.New("the expiry is in the past: a token that has expired already would be of no use")
	}
	return at.UTC().Truncate(time.Second), nil
}

// ParseSince reads how far back to look: a number of days or hours ("7d",
// "24h"), a date (from the start of that day, UTC), or a time in RFC 3339.
func ParseSince(s string, now time.Time) (time.Time, error) {
	if span, ok := parseSpan(s); ok {
		return now.Add(-span).UTC(), nil
	}
	if day, err := time.Parse(dateLayout, s); err == nil {
		return day, nil
	}
	at, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}, fmt.Errorf("invalid time %q: use days or hours back from now, or a date, e.g. 7d, 24h or %s", s, now.AddDate(0, 0, -7).UTC().Format(dateLayout))
	}
	return at.UTC(), nil
}

// ExpiryWarning is how long before a token expires the CLI and the dashboard
// start saying so.
const ExpiryWarning = 14 * 24 * time.Hour
