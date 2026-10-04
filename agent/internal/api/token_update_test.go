package api

import (
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestATokensApplicationsAreReplacedAndItsValueKeepsWorking(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/my-api/deploy", validConfig)
	f.do("POST", "/api/v1/applications/web/deploy", otherConfig)
	f.engine.Wait()
	ci := f.tokenFor(`{"name": "ci", "role": "deploy", "applications": ["my-api"]}`)
	deployWeb := func() (int, []byte) { return f.doWithAuth("POST", "/api/v1/applications/web/stop", "", ci) }

	if status, body := deployWeb(); status != http.StatusForbidden || decodeError(t, body).Code != api.CodeTokenLimited {
		t.Fatalf("before the change: status = %d, body = %s", status, body)
	}

	// Replaced, not added to: the list is what the body says, sorted, each once.
	status, body := f.do("PUT", "/api/v1/tokens/ci", `{"applications": ["web", "worker", "web"]}`)
	token := decode[api.Token](t, body)
	if status != http.StatusOK || token.Name != "ci" || token.Role != api.RoleDeploy || !reflect.DeepEqual(token.Applications, []string{"web", "worker"}) || token.ExpiresAt != nil {
		t.Fatalf("status = %d, token = %+v, body = %s", status, token, body)
	}
	if strings.Contains(string(body), "hash") || strings.Contains(string(body), api.TokenPrefix) {
		t.Errorf("the answer carries the token or its hash: %s", body)
	}
	// The same value, and from its next request what the token may do now.
	if status, body := deployWeb(); status != http.StatusOK {
		t.Errorf("after the change: status = %d, body = %s", status, body)
	}
	if status, body := f.doWithAuth("POST", "/api/v1/applications/my-api/stop", "", ci); status != http.StatusForbidden || decodeError(t, body).Details["application"] != "my-api" {
		t.Errorf("the application it lost: status = %d, body = %s", status, body)
	}

	// The limit lifted.
	status, body = f.do("PUT", "/api/v1/tokens/ci", `{"applications": []}`)
	if token := decode[api.Token](t, body); status != http.StatusOK || token.Applications == nil || len(token.Applications) != 0 {
		t.Fatalf("lifting the limit: status = %d, body = %s", status, body)
	}
	if status, body := f.doWithAuth("POST", "/api/v1/applications/my-api/stop", "", ci); status != http.StatusOK {
		t.Errorf("without a limit: status = %d, body = %s", status, body)
	}

	// The trail says what it was and what it is, and who did it.
	var got []string
	for _, e := range f.audit("?action=token.update") {
		got = append(got, e.Actor.Name+" "+e.Target+" "+e.Outcome+": "+e.Detail)
	}
	want := []string{
		"root ci ok: applications web worker -> all",
		"root ci ok: applications my-api -> web worker",
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("audit trail = %q, want %q", got, want)
	}
	if !strings.Contains(f.logs.String(), `msg="token changed" token=ci`) {
		t.Errorf("the change is not in the log:\n%s", f.logs)
	}
}

func TestAnExpiredTokenWhoseEndIsMovedWorksAgainAndTheTrailSaysWhoMovedIt(t *testing.T) {
	f := newFixture(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	ci := f.tokenFor(`{"name": "ci", "role": "deploy", "expires_at": "2026-10-02T12:00:00Z"}`)
	admin := f.tokenFor(`{"name": "ops", "role": "admin"}`)

	clock = clock.Add(48 * time.Hour)
	if status, body := f.doWithAuth("GET", "/api/v1/applications", "", ci); status != http.StatusUnauthorized || decodeError(t, body).Code != api.CodeTokenExpired {
		t.Fatalf("an expired token: status = %d, body = %s", status, body)
	}

	// An end in the past is not a way to stop a token.
	status, body := f.doWithAuth("PUT", "/api/v1/tokens/ci", `{"expires_at": "2026-10-03T11:00:00Z"}`, admin)
	if e := decodeError(t, body); status != http.StatusBadRequest || !strings.Contains(e.Message, "expires_at is in the past. To stop a token now, revoke it") {
		t.Errorf("an end in the past: status = %d, error = %+v", status, e)
	}

	status, body = f.doWithAuth("PUT", "/api/v1/tokens/ci", `{"expires_at": "2027-01-01T00:00:00+02:00"}`, admin)
	want := time.Date(2026, 12, 31, 22, 0, 0, 0, time.UTC)
	if token := decode[api.Token](t, body); status != http.StatusOK || token.ExpiresAt == nil || !token.ExpiresAt.Equal(want) {
		t.Fatalf("moving the end: status = %d, body = %s", status, body)
	}
	if status, body := f.doWithAuth("GET", "/api/v1/server", "", ci); status != http.StatusOK || !decode[api.Server](t, body).Token.ExpiresAt.Equal(want) {
		t.Errorf("the token after its end was moved: status = %d, body = %s", status, body)
	}

	status, body = f.doWithAuth("PUT", "/api/v1/tokens/ci", `{"never_expires": true}`, admin)
	if token := decode[api.Token](t, body); status != http.StatusOK || token.ExpiresAt != nil {
		t.Errorf("taking the end away: status = %d, body = %s", status, body)
	}
	// Asking for what is already so is answered, and recorded as such.
	f.doWithAuth("PUT", "/api/v1/tokens/ci", `{"never_expires": true}`, admin)

	var got []string
	for _, e := range f.audit("?action=token.update") {
		got = append(got, e.Actor.Name+" "+e.Target+" "+e.Outcome+": "+e.Detail)
	}
	wantTrail := []string{
		"ops ci ok: nothing changed",
		"ops ci ok: expires 2026-12-31T22:00:00Z -> never",
		"ops ci ok: expires 2026-10-02T12:00:00Z -> 2026-12-31T22:00:00Z",
		"ops ci failed: ",
	}
	if !reflect.DeepEqual(got, wantTrail) {
		t.Errorf("audit trail = %q, want %q", got, wantTrail)
	}
}

func TestATokenIsChangedOnlyWithinWhatCreationAllows(t *testing.T) {
	f := newFixture(t)
	f.tokenFor(`{"name": "ci", "role": "deploy", "applications": ["my-api"]}`)
	viewer := f.tokenFor(`{"name": "viewer", "role": "read"}`)
	f.createToken("ops", api.RoleAdmin)

	fifty := make([]string, 51)
	for i := range fifty {
		fifty[i] = `"app-` + string(rune('a'+i/26)) + string(rune('a'+i%26)) + `"`
	}
	for name, tt := range map[string]struct {
		path, body string
		status     int
		want       string
	}{
		"the root token":             {"/api/v1/tokens/root", `{"applications": ["web"]}`, 400, "the root token is the one the agent is configured with"},
		"a name that is not one":     {"/api/v1/tokens/Not_A_Name", `{"applications": ["web"]}`, 400, "invalid token name"},
		"a token that is not there":  {"/api/v1/tokens/nobody", `{"applications": ["web"]}`, 404, "not found"},
		"nothing to change":          {"/api/v1/tokens/ci", ``, 400, "nothing to change"},
		"an empty object":            {"/api/v1/tokens/ci", `{}`, 400, "nothing to change"},
		"the role":                   {"/api/v1/tokens/ci", `{"role": "admin"}`, 400, `unknown field "role"`},
		"the name":                   {"/api/v1/tokens/ci", `{"name": "other", "applications": []}`, 400, `unknown field "name"`},
		"an application's name":      {"/api/v1/tokens/ci", `{"applications": ["Not_Valid"]}`, 400, "applications: "},
		"too many applications":      {"/api/v1/tokens/ci", `{"applications": [` + strings.Join(fifty, ",") + `]}`, 400, "at most 50 applications"},
		"a read token limited":       {"/api/v1/tokens/viewer", `{"applications": ["web"]}`, 400, "only a deploy token can be limited to applications"},
		"an admin token limited":     {"/api/v1/tokens/ops", `{"applications": ["web"]}`, 400, "only a deploy token can be limited to applications"},
		"an end and none":            {"/api/v1/tokens/ci", `{"expires_at": "2099-01-01T00:00:00Z", "never_expires": true}`, 400, "contradict each other"},
		"an end that is not a time":  {"/api/v1/tokens/ci", `{"expires_at": "tomorrow"}`, 400, "invalid JSON body"},
		"applications that are null": {"/api/v1/tokens/ci", `{"applications": null}`, 400, "nothing to change"},
	} {
		status, body := f.do("PUT", tt.path, tt.body)
		if e := decodeError(t, body); status != tt.status || !strings.Contains(e.Message, tt.want) {
			t.Errorf("%s: status = %d, error = %+v; want %d and %q", name, status, e, tt.status, tt.want)
		}
	}
	// Nothing of that changed anything.
	_, body := f.do("GET", "/api/v1/tokens", "")
	for _, token := range decode[[]api.Token](t, body) {
		if want := map[string][]string{"ci": {"my-api"}, "viewer": {}, "ops": {}}[token.Name]; !reflect.DeepEqual(token.Applications, want) || token.ExpiresAt != nil {
			t.Errorf("token %s = %+v after refused changes", token.Name, token)
		}
	}

	// An end can be given to a token of any role; a limit only to deploy.
	if status, body := f.do("PUT", "/api/v1/tokens/viewer", `{"expires_at": "2099-01-01T00:00:00Z"}`); status != http.StatusOK {
		t.Errorf("an end for a read token: status = %d, body = %s", status, body)
	}

	// Changing tokens is admin's, and a refusal is in the trail.
	status, body := f.doWithAuth("PUT", "/api/v1/tokens/ci", `{"applications": []}`, viewer)
	if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeForbidden || e.Details["required"] != "admin" {
		t.Errorf("a read token changing a token: status = %d, error = %+v", status, e)
	}
	if entries := f.audit("?action=token.update&outcome=refused"); len(entries) != 1 || entries[0].Actor.Name != "viewer" || entries[0].Target != "ci" {
		t.Errorf("the refusal in the trail: %+v", entries)
	}
}
