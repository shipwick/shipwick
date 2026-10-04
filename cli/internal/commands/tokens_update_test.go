package commands

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// updateAgent serves PUT /tokens/{name} for a token named ci and keeps what
// it was sent.
func newUpdateAgent(t *testing.T) (*fakeAgent, *string) {
	t.Helper()
	body := new(string)
	a := &fakeAgent{t: t}
	a.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		a.requests = append(a.requests, r.Method+" "+r.URL.Path)
		if r.Method != "PUT" || r.URL.Path != "/api/v1/tokens/ci" {
			respondError(w, 404, api.Error{Code: api.CodeNotFound, Message: "not found"})
			return
		}
		raw, _ := io.ReadAll(r.Body)
		*body = strings.TrimSpace(string(raw))
		var req api.UpdateTokenRequest
		json.Unmarshal(raw, &req)
		token := api.Token{ID: 3, Name: "ci", Role: api.RoleDeploy, CreatedAt: fixedNow, Applications: []string{"my-api"}}
		if req.Applications != nil {
			token.Applications = *req.Applications
		}
		token.ExpiresAt = req.ExpiresAt
		respond(w, 200, token)
	}))
	t.Cleanup(a.srv.Close)
	return a, body
}

func TestTokenUpdateSendsOnlyWhatIsToChange(t *testing.T) {
	a, body := newUpdateAgent(t)

	out, _, err := a.run(t.TempDir(), "token", "update", "ci", "--app", "web", "--app", "my-api", "--app", "web")
	if err != nil || *body != `{"applications":["my-api","web"]}` {
		t.Fatalf("--app: %v, sent %s\n%s", err, *body, out)
	}
	assertInOrder(t, out, []string{"Changed token ci", "It is limited to my-api, web."})
	if strings.Contains(out, "expire") {
		t.Errorf("the end was not asked about:\n%s", out)
	}

	out, _, err = a.run(t.TempDir(), "token", "update", "ci", "--all-apps")
	if err != nil || *body != `{"applications":[]}` {
		t.Fatalf("--all-apps: %v, sent %s", err, *body)
	}
	assertInOrder(t, out, []string{"Changed token ci", "It is not limited: it may change every application."})

	// fixedNow is 2026-03-01 12:00 UTC.
	out, _, err = a.run(t.TempDir(), "token", "update", "ci", "--expires", "90d")
	if err != nil || *body != `{"expires_at":"2026-05-30T12:00:00Z"}` {
		t.Fatalf("--expires: %v, sent %s", err, *body)
	}
	expires := time.Date(2026, 5, 30, 12, 0, 0, 0, time.UTC)
	assertInOrder(t, out, []string{"Changed token ci", "It expires on " + expires.Local().Format("2006-01-02 at 15:04") + ", in 90 days."})

	out, _, err = a.run(t.TempDir(), "token", "update", "ci", "--no-expiry", "--all-apps")
	if err != nil || *body != `{"applications":[],"never_expires":true}` {
		t.Fatalf("--no-expiry: %v, sent %s", err, *body)
	}
	assertInOrder(t, out, []string{"It is not limited", "It does not expire."})

	for want, args := range map[string][]string{
		"say what to change: --app or --all-apps, --expires or --no-expiry": {"token", "update", "ci"},
		"--app limits the token and --all-apps lifts the limit: use one":    {"token", "update", "ci", "--app", "web", "--all-apps"},
		"--expires sets an end and --no-expiry takes it away: use one":      {"token", "update", "ci", "--expires", "90d", "--no-expiry"},
		"--app: applications: ":                                  {"token", "update", "ci", "--app", "Not_Valid"},
		"--expires: the expiry is in the past":                   {"token", "update", "ci", "--expires", "2026-02-28"},
		"the root token is the one the agent is configured with": {"token", "update", "root", "--all-apps"},
		"invalid token name":                                     {"token", "update", "Not_A_Name", "--all-apps"},
	} {
		a.requests = nil
		if _, _, err := a.run(t.TempDir(), args...); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
		if len(a.requests) != 0 {
			t.Errorf("%v: nothing should reach the agent, saw %v", args, a.requests)
		}
	}

	_, _, err = a.run(t.TempDir(), "token", "update", "other", "--all-apps")
	if want := "Error: there is no token named other\n\nList the tokens with: shipwick token ls"; Render(err) != want {
		t.Errorf("an unknown token:\n%s", Render(err))
	}
}

func TestTokenUpdateSaysWhenTheAgentCannotChangeATokenYet(t *testing.T) {
	// An agent from before knows the path for DELETE only, and answers
	// another method the way net/http does: 405, without the API's envelope.
	a := newTokenAgent(t)
	_, _, err := a.run(t.TempDir(), "token", "update", "ci", "--all-apps")
	want := "Error: the agent is older than this shipwick and cannot change a token: nothing was changed\n\n" +
		"Compare versions with: shipwick server status\nUntil it is upgraded, create a new token and revoke the old one"
	if got := Render(err); got != want {
		t.Errorf("got:\n%s\nwant:\n%s", got, want)
	}
}
