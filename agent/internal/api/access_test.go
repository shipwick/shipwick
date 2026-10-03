package api

import (
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// tokenFor creates a token from a POST /tokens body and returns its
// Authorization header.
func (f *fixture) tokenFor(body string) string {
	f.t.Helper()
	status, answer := f.do("POST", "/api/v1/tokens", body)
	if status != http.StatusCreated {
		f.t.Fatalf("create token %s: status = %d, body = %s", body, status, answer)
	}
	return "Bearer " + decode[api.CreatedToken](f.t, answer).Token
}

const otherConfig = "name: web\nimage: nginx:1\nport: 80\n"

func TestALimitedTokenChangesItsApplicationsAndReadsTheRest(t *testing.T) {
	f := newFixture(t)
	f.do("POST", "/api/v1/applications/web/deploy", otherConfig)
	f.engine.Wait()
	auth := f.tokenFor(`{"name": "ci", "role": "deploy", "applications": ["my-api", "worker"]}`)

	// my-api does not exist yet: the name being created is what is checked.
	status, body := f.doWithAuth("POST", "/api/v1/applications/my-api/deploy", validConfig, auth)
	if d := decode[api.Deployment](t, body); status != http.StatusAccepted || d.By != "ci" {
		t.Fatalf("deploy its own application: status = %d, body = %s", status, body)
	}
	f.engine.Wait()
	if status, body := f.doWithAuth("POST", "/api/v1/applications/my-api/stop", "", auth); status != http.StatusOK {
		t.Errorf("stop its own application: status = %d, body = %s", status, body)
	}

	for _, tt := range []struct{ method, path, body string }{
		{"POST", "/api/v1/applications/web/deploy", otherConfig},
		{"POST", "/api/v1/applications/web/redeploy", ""},
		{"POST", "/api/v1/applications/web/stop", ""},
		{"POST", "/api/v1/applications/web/run", `{"command": ["true"]}`},
		{"POST", "/api/v1/applications/web/validate", otherConfig},
		{"POST", "/api/v1/applications/new-one/deploy", "name: new-one\nimage: nginx:1\n"},
	} {
		status, body := f.doWithAuth(tt.method, tt.path, tt.body, auth)
		e := decodeError(t, body)
		if status != http.StatusForbidden || e.Code != api.CodeTokenLimited {
			t.Errorf("%s %s: status = %d, error = %+v; want 403 %s", tt.method, tt.path, status, e, api.CodeTokenLimited)
			continue
		}
		name := strings.Split(tt.path, "/")[4]
		if e.Message != "this token is limited to my-api and worker; it can read "+name+" and not change it" || e.Details["application"] != name ||
			!reflect.DeepEqual(e.Details["applications"], []any{"my-api", "worker"}) {
			t.Errorf("%s %s: error = %+v", tt.method, tt.path, e)
		}
	}
	if apps := decodeList(t, f); len(apps) != 2 {
		t.Errorf("deployments = %d, want 2: a refused request starts nothing", len(apps))
	}

	for _, path := range []string{"/api/v1/applications", "/api/v1/applications/web", "/api/v1/applications/web/events", "/api/v1/deployments", "/api/v1/server", "/api/v1/secrets"} {
		if status, body := f.doWithAuth("GET", path, "", auth); status != http.StatusOK {
			t.Errorf("GET %s: status = %d, body = %s; a limited token reads everything", path, status, body)
		}
	}

	// What admin does stays admin's, and is refused for the role, not the limit.
	status, body = f.doWithAuth("DELETE", "/api/v1/applications/my-api", "", auth)
	if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeForbidden {
		t.Errorf("delete its own application: status = %d, error = %+v; want 403 %s", status, e, api.CodeForbidden)
	}
}

func TestOnlyADeployTokenCanBeLimited(t *testing.T) {
	f := newFixture(t)
	many := make([]string, api.MaxTokenApplications+1)
	for i := range many {
		many[i] = `"app-` + string(rune('a'+i/26)) + string(rune('a'+i%26)) + `"`
	}
	for name, tt := range map[string]struct{ body, message string }{
		"read":       {`{"name": "t", "role": "read", "applications": ["web"]}`, "only a deploy token can be limited to applications: a read token changes nothing, and admin is for the whole server"},
		"admin":      {`{"name": "t", "role": "admin", "applications": ["web"]}`, "only a deploy token can be limited to applications: a read token changes nothing, and admin is for the whole server"},
		"bad name":   {`{"name": "t", "role": "deploy", "applications": ["Web_1"]}`, "applications: "},
		"too many":   {`{"name": "t", "role": "deploy", "applications": [` + strings.Join(many, ",") + `]}`, "a token can be limited to at most 50 applications"},
		"past":       {`{"name": "t", "role": "deploy", "expires_at": "2020-01-01T00:00:00Z"}`, "expires_at is in the past: a token that has expired already would be of no use"},
		"not a time": {`{"name": "t", "role": "deploy", "expires_at": "tomorrow"}`, "invalid JSON body: "},
	} {
		status, body := f.do("POST", "/api/v1/tokens", tt.body)
		if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest || !strings.HasPrefix(e.Message, tt.message) {
			t.Errorf("%s: status = %d, error = %+v", name, status, e)
		}
	}
	if _, body := f.do("GET", "/api/v1/tokens", ""); len(decode[[]api.Token](t, body)) != 0 {
		t.Errorf("refused requests created tokens: %s", body)
	}

	status, body := f.do("POST", "/api/v1/tokens", `{"name": "ci", "role": "deploy", "applications": ["web", "api", "web"]}`)
	created := decode[api.CreatedToken](t, body)
	if status != http.StatusCreated || !reflect.DeepEqual(created.Applications, []string{"api", "web"}) || created.ExpiresAt != nil {
		t.Errorf("status = %d, created = %+v; want the applications sorted, each once", status, created)
	}
	_, body = f.do("GET", "/api/v1/tokens", "")
	if tokens := decode[[]api.Token](t, body); len(tokens) != 1 || !reflect.DeepEqual(tokens[0].Applications, []string{"api", "web"}) || tokens[0].ExpiresAt != nil {
		t.Errorf("tokens = %+v", tokens)
	}
	if !strings.Contains(string(body), `"expires_at":null`) {
		t.Errorf("a token that does not expire says null: %s", body)
	}
}

func TestAnExpiredTokenIsRefusedWithItsOwnCodeAndNotCountedAsGuessing(t *testing.T) {
	f := newFixture(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	auth := f.tokenFor(`{"name": "ci", "role": "deploy", "expires_at": "2026-10-02T12:00:00Z"}`)

	clock = clock.Add(24*time.Hour - time.Second)
	status, body := f.doWithAuth("GET", "/api/v1/server", "", auth)
	server := decode[api.Server](t, body)
	if status != http.StatusOK || server.Token.ExpiresAt == nil || !server.Token.ExpiresAt.Equal(clock.Add(time.Second)) {
		t.Fatalf("a second before it expires: status = %d, token = %+v", status, server.Token)
	}

	clock = clock.Add(time.Second)
	h := f.api.Handler()
	const addr = "198.51.100.7"
	for i := range failedAuthLimit + 5 {
		rec := f.from(h, addr, "/api/v1/applications", auth)
		e := decodeError(t, rec.Body.Bytes())
		if rec.Code != http.StatusUnauthorized || e.Code != api.CodeTokenExpired {
			t.Fatalf("attempt %d: status = %d, error = %+v; want 401 %s every time", i+1, rec.Code, e, api.CodeTokenExpired)
		}
		if e.Message != "token ci expired on 2026-10-02 at 12:00 UTC; an admin creates a new one with: shipwick token create" ||
			e.Details["expired_at"] != "2026-10-02T12:00:00Z" || e.Details["name"] != "ci" {
			t.Fatalf("error = %+v", e)
		}
	}
	// Twenty-five refusals later the address is not limited: a wrong token
	// is still answered 401, not 429.
	if rec := f.from(h, addr, "/api/v1/applications", "Bearer wrong"); rec.Code != http.StatusUnauthorized || decodeError(t, rec.Body.Bytes()).Code != api.CodeUnauthorized {
		t.Errorf("a wrong token after the expired one: status = %d, body = %s", rec.Code, rec.Body)
	}

	// The token stays listed, expired, and its refusals are not uses.
	_, body = f.do("GET", "/api/v1/tokens", "")
	tokens := decode[[]api.Token](t, body)
	if len(tokens) != 1 || tokens[0].ExpiresAt == nil || !tokens[0].ExpiresAt.Equal(clock) {
		t.Fatalf("tokens = %+v", tokens)
	}
	if used := tokens[0].LastUsedAt; used == nil || !used.Before(clock) {
		t.Errorf("last used = %v, want the use before it expired", used)
	}
}

func TestAWrongTokenLearnsNothingAboutExpiry(t *testing.T) {
	f := newFixture(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	f.tokenFor(`{"name": "ci", "role": "deploy", "expires_at": "2026-10-01T13:00:00Z"}`)
	clock = clock.Add(2 * time.Hour)

	status, body := f.doWithAuth("GET", "/api/v1/applications", "", "Bearer swk_someone-elses-guess")
	if e := decodeError(t, body); status != http.StatusUnauthorized || e.Code != api.CodeUnauthorized || e.Message != "missing or invalid API token" || len(e.Details) != 0 {
		t.Errorf("status = %d, error = %+v", status, e)
	}
}

func TestTheRootTokenDoesNotExpireAndIsNotLimited(t *testing.T) {
	f := newFixture(t)
	clock := time.Date(2126, 1, 1, 0, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	status, body := f.do("GET", "/api/v1/server", "")
	if status != http.StatusOK || !strings.Contains(string(body), `"token":{"name":"root","role":"admin","kind":"token","applications":[],"expires_at":null}`) {
		t.Errorf("status = %d, body = %s", status, body)
	}
}

func TestServerSaysWhatTheCallerMayDo(t *testing.T) {
	f := newFixture(t)
	clock := time.Date(2026, 10, 1, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	auth := f.tokenFor(`{"name": "ci", "role": "deploy", "applications": ["my-api"], "expires_at": "2026-12-30T12:00:00Z"}`)

	_, body := f.doWithAuth("GET", "/api/v1/server", "", auth)
	want := api.TokenIdentity{Kind: api.ActorToken, Name: "ci", Role: api.RoleDeploy, Applications: []string{"my-api"}}
	got := decode[api.Server](t, body).Token
	if got.ExpiresAt == nil || !got.ExpiresAt.Equal(time.Date(2026, 12, 30, 12, 0, 0, 0, time.UTC)) {
		t.Errorf("expires_at = %v", got.ExpiresAt)
	}
	got.ExpiresAt = nil
	if !reflect.DeepEqual(got, want) {
		t.Errorf("token = %+v, want %+v", got, want)
	}
}

// pathFor fills a route's pattern in: the application is `name`, every other
// segment something its handler would accept.
func pathFor(rt route, name string) string {
	path := strings.Replace(rt.path, "{name}", name, 1)
	for segment, value := range map[string]string{"{id}": "1", "{job}": "nightly", "{volume}": "data", "{hostname}": "example.com", "{registry}": "ghcr.io"} {
		path = strings.Replace(path, segment, value, 1)
	}
	return path
}

func TestEveryApplicationRouteIsCheckedAgainstTheTokensApplications(t *testing.T) {
	f := newFixture(t)
	auth := f.tokenFor(`{"name": "ci", "role": "deploy", "applications": ["mine"]}`)

	checked := 0
	for _, rt := range f.api.routes {
		if strings.Contains(rt.path, "/applications/{") != rt.application {
			t.Errorf("%s: acts on an application and is not under %s, so the application check does not see it", rt.pattern(), applicationPrefix)
			continue
		}
		if !rt.application {
			if rt.role == api.RoleDeploy {
				// Nothing server-wide takes deploy today. One that does is
				// refused to a limited token: see the test below.
				if no := authorize(api.TokenIdentity{Role: api.RoleDeploy, Applications: []string{"mine"}}, rt, httptest.NewRequest(rt.method, pathFor(rt, "x"), nil)); no == nil {
					t.Errorf("%s: a limited token may use a server-wide operation", rt.pattern())
				}
			}
			continue
		}
		status, body := f.doWithAuth(rt.method, pathFor(rt, "other"), "", auth)
		switch rt.role {
		case api.RoleRead:
			if status == http.StatusForbidden || status == http.StatusUnauthorized {
				t.Errorf("%s: status = %d; reading is never limited", rt.pattern(), status)
			}
		case api.RoleDeploy:
			checked++
			if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeTokenLimited || e.Details["application"] != "other" {
				t.Errorf("%s on another application: status = %d, body = %s; want 403 %s", rt.pattern(), status, body, api.CodeTokenLimited)
			}
			// The same request for its own application gets past the check.
			status, body = f.doWithAuth(rt.method, pathFor(rt, "mine"), "", auth)
			if status == http.StatusForbidden || status == http.StatusUnauthorized {
				t.Errorf("%s on its own application: status = %d, body = %s", rt.pattern(), status, body)
			}
		case api.RoleAdmin:
			if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeForbidden {
				t.Errorf("%s: status = %d, body = %s; want 403 %s", rt.pattern(), status, body, api.CodeForbidden)
			}
		}
	}
	if checked < 10 {
		t.Errorf("only %d deploy routes were walked: the route table did not register", checked)
	}
}

func TestAServerWideOperationIsRefusedToALimitedToken(t *testing.T) {
	limited := api.TokenIdentity{Kind: api.ActorToken, Name: "ci", Role: api.RoleDeploy, Applications: []string{"my-api"}}
	whole := api.TokenIdentity{Kind: api.ActorToken, Name: "ops", Role: api.RoleDeploy, Applications: []string{}}
	// An admin with a list is still an admin: the list is not consulted.
	admin := api.TokenIdentity{Kind: api.ActorToken, Name: "root", Role: api.RoleAdmin, Applications: []string{"my-api"}}

	everything := newRoute("POST /api/v1/deploy", api.RoleDeploy)
	r := httptest.NewRequest("POST", "/api/v1/deploy", nil)
	no := authorize(limited, everything, r)
	if no == nil || no.code != api.CodeTokenLimited || no.message != "this token is limited to my-api, and this is not about one application" {
		t.Errorf("limited token, server-wide deploy: %+v", no)
	}
	for _, who := range []api.TokenIdentity{whole, admin} {
		if no := authorize(who, everything, r); no != nil {
			t.Errorf("%s: refused: %+v", who.Name, no)
		}
	}
	one := newRoute("POST /api/v1/applications/{name}/deploy", api.RoleDeploy)
	r = httptest.NewRequest("POST", "/api/v1/applications/web/deploy", nil)
	r.SetPathValue("name", "web")
	if no := authorize(admin, one, r); no != nil {
		t.Errorf("admin is never limited: %+v", no)
	}
	if no := authorize(limited, one, r); no == nil || no.code != api.CodeTokenLimited {
		t.Errorf("limited token on another application: %+v", no)
	}
	if no := authorize(limited, newRoute("GET /api/v1/tokens", api.RoleAdmin), r); no == nil || no.code != api.CodeForbidden {
		t.Errorf("the role is judged first: %+v", no)
	}
}
