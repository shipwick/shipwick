package commands

import (
	"fmt"
	"math"
	"strings"
	"time"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

// auditSubject is what an entry was about: the application, and what else
// the request named.
func auditSubject(e api.AuditEntry) string {
	switch {
	case e.Application != "" && e.Target != "":
		return e.Application + " " + e.Target
	case e.Application != "":
		return e.Application
	case e.Target != "":
		return e.Target
	}
	return "server"
}

func auditResult(e api.AuditEntry) ui.Cell {
	switch e.Outcome {
	case api.AuditOK:
		return ui.C("ok")
	case api.AuditRefused:
		return ui.Cell{Text: "refused", Style: ui.Yellow}
	}
	if e.Code != "" {
		return ui.Cell{Text: "failed: " + e.Code, Style: ui.Red}
	}
	return ui.Cell{Text: "failed", Style: ui.Red}
}

// auditOrigin prefers the client the proxy reported over the proxy's own
// address, which is the same for everyone who came through it.
func auditOrigin(e api.AuditEntry) string {
	if e.ForwardedFor != "" {
		return e.ForwardedFor
	}
	return e.Address
}

// expiryText says when a token expires, and whether that is worth a
// warning: within api.ExpiryWarning, or past.
func expiryText(at *time.Time, now time.Time) (text string, style ui.Style) {
	if at == nil {
		return "never", ui.Plain
	}
	left := at.Sub(now)
	switch {
	case left <= 0:
		return "expired " + ui.RelativeTime(*at, now), ui.Red
	case left <= api.ExpiryWarning:
		return "in " + span(left) + " (soon)", ui.Yellow
	}
	return "in " + span(left), ui.Plain
}

// span is a time left, in the largest unit that says something: "90 days",
// "36 hours", "20 minutes". It is rounded, so that ninety days are still
// ninety a moment after they were asked for.
func span(d time.Duration) string {
	switch {
	case d >= 48*time.Hour:
		return plural(int(math.Round(d.Hours()/24)), "day")
	case d >= 2*time.Hour:
		return plural(int(math.Round(d.Hours())), "hour")
	}
	return plural(max(1, int(math.Round(d.Minutes()))), "minute")
}

// describeCaller is the token a command runs as, with what narrows it:
// "ci (deploy, limited to my-api, expires in 89 days)".
func describeCaller(who api.TokenIdentity, now time.Time) string {
	parts := []string{string(who.Role)}
	if who.Limited() {
		parts = append(parts, "limited to "+strings.Join(who.Applications, ", "))
	}
	if who.ExpiresAt != nil {
		if left := who.ExpiresAt.Sub(now); left > 0 {
			parts = append(parts, "expires in "+span(left))
		}
	}
	return who.Name + " (" + strings.Join(parts, ", ") + ")"
}

// token reports the token doctor ran with, and says so while there is still
// time when it is about to expire.
func (r *report) token(who api.TokenIdentity, now time.Time) {
	if who.ExpiresAt != nil && who.ExpiresAt.Sub(now) <= api.ExpiryWarning {
		r.hint("Token %s. From %s it is refused; create its replacement before then: %s",
			describeCaller(who, now), who.ExpiresAt.Local().Format("2006-01-02 15:04"), replacementCommand(who))
		return
	}
	r.ok("Token %s", describeCaller(who, now))
}

// replacementCommand creates a token that may do what who may.
func replacementCommand(who api.TokenIdentity) string {
	cmd := "shipwick token create <name> --role " + string(who.Role)
	if who.Limited() {
		cmd += " --app " + strings.Join(who.Applications, " --app ")
	}
	return cmd + " --expires 90d"
}

// renderTokenExpired tells the holder of an expired token that it is the
// right token and its time is up, which a wrong token is never told.
func renderTokenExpired(e *client.APIError) string {
	what, admin := "The API token has expired.", "An admin creates"
	name, _ := e.Details["name"].(string)
	raw, _ := e.Details["expired_at"].(string)
	// The name goes into a command to copy: only what a token can be called.
	if at, err := time.Parse(time.RFC3339, raw); err == nil && api.ValidateTokenName(name) == nil {
		what = fmt.Sprintf("The token %s expired on %s.", name, at.Local().Format("2006-01-02 at 15:04"))
		admin = "An admin lets it work again with: shipwick token update " + name + " --expires 90d\nOr creates"
	}
	return what + "\n\n" + admin + " a new one with: shipwick token create\nThen set " + cliconfig.EnvToken + " to it, or save it with: shipwick login"
}

// renderTokenLimited tells the user which applications the token may change.
func renderTokenLimited(e *client.APIError) string {
	var applications []string
	if list, ok := e.Details["applications"].([]any); ok {
		for _, v := range list {
			if name, ok := v.(string); ok {
				applications = append(applications, name)
			}
		}
	}
	if len(applications) == 0 {
		return "This token may not do that: " + e.Message
	}
	limit := "This token may not do that: it is limited to " + strings.Join(applications, ", ") + "."
	if app, _ := e.Details["application"].(string); app != "" {
		return limit + "\n\nIt can read " + app + ", not change it. Use a token that covers it, or create one with: shipwick token create <name> --role deploy --app " + app
	}
	return limit + "\n\nThis operation is not about one application. Use a deploy token without --app, or an admin token."
}
