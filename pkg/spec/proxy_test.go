package spec

import (
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
)

const proxyBase = "name: api\nimage: app:1\nport: 8080\ndomain: example.com\n"

// wantFieldError asserts that config is refused with msg on field.
func wantFieldError(t *testing.T, config, field, msg string) {
	t.Helper()
	_, err := Parse([]byte(config))
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v, want a validation error on %s", err, field)
	}
	for _, f := range verr.Fields {
		if f.Field == field && strings.Contains(f.Message, msg) {
			return
		}
	}
	t.Errorf("no error on %s containing %q in %+v", field, msg, verr.Fields)
}

func TestPath(t *testing.T) {
	for in, want := range map[string]string{
		"/api":         "/api",
		"/docs/v2":     "/docs/v2",
		"/a.b_c~d-e/F": "/a.b_c~d-e/F",
		"/":            "",
		`""`:           "",
	} {
		app, err := Parse([]byte(proxyBase + "path: " + in + "\n"))
		if err != nil {
			t.Errorf("%s: %v", in, err)
			continue
		}
		if app.Path != want {
			t.Errorf("%s: Path = %q, want %q", in, app.Path, want)
		}
	}

	for _, tt := range []struct{ name, path, msg string }{
		{"no leading slash", "api", "must start with /"},
		{"trailing slash", "/api/", "must not end with /"},
		{"empty segment", "/api//v2", "between the slashes"},
		{"a space", `"/my api"`, "between the slashes"},
		{"a wildcard", `"/api/*"`, "between the slashes"},
		{"a placeholder of the proxy", `"/{http.request.host}"`, "between the slashes"},
		{"percent-encoding", "/a%2Fb", "between the slashes"},
		{"dot segments", "/api/../admin", ". or .. segments"},
		{"too long", "/" + strings.Repeat("a", 200), "longer than 200"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantFieldError(t, proxyBase+"path: "+tt.path+"\n", "path", tt.msg)
		})
	}
}

func TestPathNeedsADomain(t *testing.T) {
	wantFieldError(t, "name: api\nimage: app:1\npath: /api\n", "path", "requires domain")
}

func TestAStaticApplicationMayHaveAPath(t *testing.T) {
	app, err := Parse([]byte("name: docs\nstatic: dist\ndomain: example.com\npath: /docs\n"))
	if err != nil || app.Path != "/docs" {
		t.Errorf("Path = %q, %v", app.Path, err)
	}
}

func TestPathWithin(t *testing.T) {
	for _, tt := range []struct {
		p, prefix string
		want      bool
	}{
		{"/api", "/api", true},
		{"/api/users", "/api", true},
		{"/API/users", "/api", true},
		{"/apix", "/api", false},
		{"/", "/api", false},
		{"/anything", "", true},
	} {
		if got := PathWithin(tt.p, tt.prefix); got != tt.want {
			t.Errorf("PathWithin(%q, %q) = %v", tt.p, tt.prefix, got)
		}
	}
}

func TestProxyBlock(t *testing.T) {
	app, err := Parse([]byte(proxyBase + `path: /api
proxy:
  strip_prefix: true
  headers:
    X-Frame-Options: DENY
    Strict-Transport-Security: max-age=31536000
  basic_auth:
    - path: /api/admin
      username: admin
      password: correct horse
    - username: everyone
      password: ${SITE_PASSWORD}
  redirects:
    - from: /api/old
      to: /api/new
    - from: /api/docs
      to: https://docs.example.org/api?from=old
      status: 301
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	p := app.Proxy
	if p == nil || !p.StripPrefix || len(p.Headers) != 2 || p.Headers["X-Frame-Options"] != "DENY" {
		t.Fatalf("proxy = %+v", p)
	}
	if want := []BasicAuth{{Path: "/api/admin", Username: "admin", Password: "correct horse"}, {Username: "everyone", Password: "${SITE_PASSWORD}"}}; fmt.Sprint(p.BasicAuth) != fmt.Sprint(want) {
		t.Errorf("basic_auth = %+v, want %+v", p.BasicAuth, want)
	}
	if want := []PathRedirect{{"/api/old", "/api/new", 308}, {"/api/docs", "https://docs.example.org/api?from=old", 301}}; fmt.Sprint(p.Redirects) != fmt.Sprint(want) {
		t.Errorf("redirects = %+v, want %+v", p.Redirects, want)
	}

	// The stored form reads back as it was written.
	stored, _ := json.Marshal(app)
	var back App
	if err := json.Unmarshal(stored, &back); err != nil || fmt.Sprint(back.Proxy) != fmt.Sprint(app.Proxy) || back.Path != "/api" {
		t.Errorf("round trip: %+v, %v", back.Proxy, err)
	}
}

func TestProxyBlockThatSaysNothingIsNoProxyBlock(t *testing.T) {
	for _, block := range []string{"", "proxy:\n", "proxy: {}\n", "proxy:\n  strip_prefix: false\n  headers: {}\n"} {
		app, err := Parse([]byte(proxyBase + block))
		if err != nil || app.Proxy != nil {
			t.Errorf("%q: Proxy = %+v, %v", block, app.Proxy, err)
		}
	}
}

func TestProxyBlockIsDecodedStrictly(t *testing.T) {
	_, err := Parse([]byte(proxyBase + "proxy:\n  header:\n    X-A: b\n  basic_auth:\n    - user: admin\n      password: long enough\n"))
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v", err)
	}
	report := verr.Error()
	for _, want := range []string{"line 6:\n  unknown field \"header\"", "line 9:\n  unknown field \"user\"", "proxy.basic_auth[0].username:\n  is required"} {
		if !strings.Contains(report, want) {
			t.Errorf("report lacks %q:\n%s", want, report)
		}
	}
	if strings.Contains(report, "spec.") {
		t.Errorf("a Go type name leaked:\n%s", report)
	}

	wantFieldError(t, proxyBase+"proxy:\n  headers: [a, b]\n", "line 6", "cannot unmarshal")
	wantFieldError(t, proxyBase+"proxy: yes please\n", "line 5", "cannot unmarshal")
}

func TestProxyValidation(t *testing.T) {
	many := func(n int, item func(i int) string) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString(item(i))
		}
		return b.String()
	}
	for _, tt := range []struct{ name, config, field, msg string }{
		{"strip without a path", "proxy:\n  strip_prefix: true\n", "proxy.strip_prefix", "requires path"},

		{"header name with a space", "proxy:\n  headers:\n    \"X Frame\": DENY\n", "proxy.headers", `invalid header name "X Frame"`},
		{"header name with a colon", "proxy:\n  headers:\n    \"X-A:\": b\n", "proxy.headers", "invalid header name"},
		{"header value with a newline", "proxy:\n  headers:\n    X-A: \"b\\r\\nSet-Cookie: x=y\"\n", "proxy.headers.X-A", "control characters"},
		{"empty header value", "proxy:\n  headers:\n    X-A: \"\"\n", "proxy.headers.X-A", "must not be empty"},
		{"header value too long", "proxy:\n  headers:\n    X-A: " + strings.Repeat("x", 4097) + "\n", "proxy.headers.X-A", "too long"},
		{"content length", "proxy:\n  headers:\n    Content-Length: 10\n", "proxy.headers.Content-Length", "belongs to the connection"},
		{"transfer encoding, in any case", "proxy:\n  headers:\n    transfer-ENCODING: chunked\n", "proxy.headers.transfer-ENCODING", "belongs to the connection"},
		{"connection", "proxy:\n  headers:\n    Connection: close\n", "proxy.headers.Connection", "belongs to the connection"},
		{"upgrade", "proxy:\n  headers:\n    Upgrade: h2c\n", "proxy.headers.Upgrade", "belongs to the connection"},
		{"one header twice", "proxy:\n  headers:\n    X-A: b\n    x-a: c\n", "proxy.headers.x-a", "same header as X-A"},
		{"too many headers", "proxy:\n  headers:\n" + many(51, func(i int) string { return fmt.Sprintf("    X-H%d: v\n", i) }), "proxy.headers", "too many (51)"},

		{"no username", "proxy:\n  basic_auth:\n    - password: long enough\n", "proxy.basic_auth[0].username", "is required"},
		{"colon in a username", "proxy:\n  basic_auth:\n    - username: \"a:b\"\n      password: long enough\n", "proxy.basic_auth[0].username", "colon"},
		{"control character in a username", "proxy:\n  basic_auth:\n    - username: \"a\\tb\"\n      password: long enough\n", "proxy.basic_auth[0].username", "control characters"},
		{"no password", "proxy:\n  basic_auth:\n    - username: admin\n", "proxy.basic_auth[0].password", "is required"},
		{"short password", "proxy:\n  basic_auth:\n    - username: admin\n      password: seven77\n", "proxy.basic_auth[0].password", "too short"},
		{"password bcrypt would cut", "proxy:\n  basic_auth:\n    - username: admin\n      password: " + strings.Repeat("x", 73) + "\n", "proxy.basic_auth[0].password", "too long"},
		{"auth path with a trailing slash", "proxy:\n  basic_auth:\n    - path: /admin/\n      username: admin\n      password: long enough\n", "proxy.basic_auth[0].path", "must not end with /"},
		{"one user twice on a path", "proxy:\n  basic_auth:\n    - {path: /admin, username: admin, password: long enough}\n    - {path: /ADMIN, username: admin, password: another one}\n", "proxy.basic_auth[1].username", "listed twice"},
		{"too many accounts", "proxy:\n  basic_auth:\n" + many(21, func(i int) string { return fmt.Sprintf("    - {username: u%d, password: long enough}\n", i) }), "proxy.basic_auth", "too many (21)"},

		{"redirect without from", "proxy:\n  redirects:\n    - to: /new\n", "proxy.redirects[0].from", "is required"},
		{"redirect of everything", "proxy:\n  redirects:\n    - {from: /, to: /new}\n", "proxy.redirects[0].from", "below /"},
		{"redirect without to", "proxy:\n  redirects:\n    - from: /old\n", "proxy.redirects[0].to", "is required"},
		{"redirect to plain http", "proxy:\n  redirects:\n    - {from: /old, to: \"http://example.org/\"}\n", "proxy.redirects[0].to", "https:// URL"},
		{"redirect to a relative path", "proxy:\n  redirects:\n    - {from: /old, to: new}\n", "proxy.redirects[0].to", "https:// URL"},
		{"redirect to another host by two slashes", "proxy:\n  redirects:\n    - {from: /old, to: //evil.example/}\n", "proxy.redirects[0].to", "one slash"},
		{"redirect to a placeholder of the proxy", "proxy:\n  redirects:\n    - {from: /old, to: \"/{env.HOME}\"}\n", "proxy.redirects[0].to", "braces"},
		{"redirect with credentials", "proxy:\n  redirects:\n    - {from: /old, to: \"https://u:p@example.org/\"}\n", "proxy.redirects[0].to", "https:// URL"},
		{"redirect status", "proxy:\n  redirects:\n    - {from: /old, to: /new, status: 303}\n", "proxy.redirects[0].status", "invalid value 303"},
		{"redirect to itself", "proxy:\n  redirects:\n    - {from: /old, to: \"/OLD?x=1\"}\n", "proxy.redirects[0].to", "to itself"},
		{"redirects in a circle", "proxy:\n  redirects:\n    - {from: /a, to: /b}\n    - {from: /b, to: /c}\n    - {from: /c, to: /a}\n", "proxy.redirects[0].to", "leads back to /a"},
		{"one path redirected twice", "proxy:\n  redirects:\n    - {from: /old, to: /new}\n    - {from: /Old, to: /newer}\n", "proxy.redirects[1].from", "redirected twice"},
		{"too many redirects", "proxy:\n  redirects:\n" + many(101, func(i int) string { return fmt.Sprintf("    - {from: /o%d, to: /n}\n", i) }), "proxy.redirects", "too many (101)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantFieldError(t, proxyBase+tt.config, tt.field, tt.msg)
		})
	}
}

func TestProxyNeedsADomain(t *testing.T) {
	wantFieldError(t, "name: api\nimage: app:1\nproxy:\n  headers:\n    X-A: b\n", "proxy", "requires domain")
}

func TestProxyPathsMustLieUnderTheApplicationsPath(t *testing.T) {
	base := proxyBase + "path: /api\n"
	wantFieldError(t, base+"proxy:\n  basic_auth:\n    - {path: /admin, username: admin, password: long enough}\n", "proxy.basic_auth[0].path", "outside path /api")
	wantFieldError(t, base+"proxy:\n  redirects:\n    - {from: /apix, to: /api}\n", "proxy.redirects[0].from", "outside path /api")
	if _, err := Parse([]byte(base + "proxy:\n  basic_auth:\n    - {path: /api, username: admin, password: long enough}\n  redirects:\n    - {from: /api/old, to: /elsewhere}\n")); err != nil {
		t.Errorf("paths under /api, and a target anywhere on the host: %v", err)
	}
}

func TestAChainOfRedirectsThatEndsIsAccepted(t *testing.T) {
	if _, err := Parse([]byte(proxyBase + "proxy:\n  redirects:\n    - {from: /a, to: /b}\n    - {from: /b, to: /c}\n")); err != nil {
		t.Error(err)
	}
}

func TestAPasswordLeftForTheServerIsNotJudgedByItsPlaceholder(t *testing.T) {
	// ${PW} is five characters; what it stands for is checked where it is
	// filled in.
	app, err := Parse([]byte(proxyBase + "proxy:\n  basic_auth:\n    - {username: admin, password: \"${PW}\"}\n"))
	if err != nil || app.Proxy.BasicAuth[0].Password != "${PW}" {
		t.Errorf("proxy = %+v, %v", app.Proxy, err)
	}
	// An escaped placeholder is text, and short text is short.
	wantFieldError(t, proxyBase+"proxy:\n  basic_auth:\n    - {username: admin, password: \"$${A}\"}\n", "proxy.basic_auth[0].password", "too short")
}

func TestValidationErrorNeverEchoesAPassword(t *testing.T) {
	_, err := Parse([]byte(proxyBase + "proxy:\n  basic_auth:\n    - {username: \"a:b\", password: hunter2}\n"))
	if err == nil || strings.Contains(err.Error(), "hunter2") {
		t.Errorf("err = %v", err)
	}
}

func TestRedactedMasksBasicAuthPasswords(t *testing.T) {
	app, err := Parse([]byte(proxyBase + "env:\n  A: b\nproxy:\n  headers:\n    X-A: b\n  basic_auth:\n    - {username: admin, password: correct horse}\n"))
	if err != nil {
		t.Fatal(err)
	}
	for _, a := range []App{app, func() App { a := app; a.Env = nil; return a }()} {
		red := a.Redacted()
		if red.Proxy.BasicAuth[0].Password != "********" || red.Proxy.BasicAuth[0].Username != "admin" || red.Proxy.Headers["X-A"] != "b" {
			t.Errorf("redacted = %+v", red.Proxy)
		}
		if out, _ := json.Marshal(red); strings.Contains(string(out), "correct horse") {
			t.Errorf("the password is in the redacted JSON: %s", out)
		}
	}
	if app.Proxy.BasicAuth[0].Password != "correct horse" {
		t.Error("Redacted changed the spec it was called on")
	}
}
