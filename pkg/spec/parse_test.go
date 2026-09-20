package spec

import (
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"
)

const fullConfig = `
name: my-api
image: ghcr.io/company/my-api:1.4.2
port: 8080
domain: api.example.com
replicas: 2

env:
  DATABASE_URL: postgres://db/app
  LOG_LEVEL: info

health:
  path: /health
  interval: 10s
  timeout: 3s
  retries: 3

resources:
  cpu: 2
  memory: 1gb

restart:
  policy: always

deploy:
  strategy: rolling
`

func TestParseFullConfig(t *testing.T) {
	app, err := Parse([]byte(fullConfig))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}

	if app.Name != "my-api" || app.Image != "ghcr.io/company/my-api:1.4.2" {
		t.Errorf("unexpected name/image: %q %q", app.Name, app.Image)
	}
	if app.Port != 8080 || app.Domain != "api.example.com" || app.Replicas != 2 {
		t.Errorf("unexpected port/domain/replicas: %d %q %d", app.Port, app.Domain, app.Replicas)
	}
	if app.Health == nil || app.Health.Path != "/health" ||
		app.Health.Interval.Std() != 10*time.Second ||
		app.Health.Timeout.Std() != 3*time.Second || app.Health.Retries != 3 {
		t.Errorf("unexpected health: %+v", app.Health)
	}
	if app.Resources.CPU != 2 || app.Resources.MemoryBytes != 1<<30 {
		t.Errorf("unexpected resources: %+v", app.Resources)
	}
	if app.Resources.NanoCPUs() != 2_000_000_000 {
		t.Errorf("NanoCPUs = %d", app.Resources.NanoCPUs())
	}
	if app.Restart.Policy != RestartAlways || app.Deploy.Strategy != StrategyRolling {
		t.Errorf("unexpected restart/deploy: %+v %+v", app.Restart, app.Deploy)
	}
	if app.Env["LOG_LEVEL"] != "info" {
		t.Errorf("unexpected env: %v", app.Env)
	}
	if app.Version() != "1.4.2" {
		t.Errorf("Version = %q", app.Version())
	}
}

func TestParseMinimalConfigAppliesDefaults(t *testing.T) {
	app, err := Parse([]byte("name: worker\nimage: redis:7\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if app.Replicas != 1 {
		t.Errorf("Replicas = %d, want 1", app.Replicas)
	}
	if app.Restart.Policy != RestartAlways {
		t.Errorf("Restart.Policy = %q", app.Restart.Policy)
	}
	if app.Deploy.Strategy != StrategyRolling {
		t.Errorf("Deploy.Strategy = %q", app.Deploy.Strategy)
	}
	if app.Health != nil {
		t.Errorf("Health = %+v, want nil", app.Health)
	}
	if app.Resources != (Resources{}) {
		t.Errorf("Resources = %+v, want zero (unlimited)", app.Resources)
	}
}

func TestParseHealthDefaults(t *testing.T) {
	app, err := Parse([]byte("name: a\nimage: nginx\nport: 80\nhealth:\n  path: /up\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	h := app.Health
	if h.Interval.Std() != DefaultHealthInterval || h.Timeout.Std() != DefaultHealthTimeout || h.Retries != DefaultHealthRetries {
		t.Errorf("unexpected defaults: %+v", h)
	}
}

func TestParseAcceptsJSON(t *testing.T) {
	app, err := Parse([]byte(`{"name":"my-api","image":"nginx:1.27","port":80,"resources":{"cpu":0.5,"memory":"256mb"}}`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if app.Resources.CPU != 0.5 || app.Resources.MemoryBytes != 256<<20 {
		t.Errorf("unexpected resources: %+v", app.Resources)
	}
}

func TestParseValidationErrors(t *testing.T) {
	tests := []struct {
		name      string
		config    string
		wantField string
		wantMsg   string
	}{
		{"missing name", "image: nginx\n", "name", "is required"},
		{"uppercase name", "name: MyApi\nimage: nginx\n", "name", `invalid value "MyApi"`},
		{"name with underscore", "name: my_api\nimage: nginx\n", "name", "invalid value"},
		{"name with path traversal", "name: ../etc\nimage: nginx\n", "name", "invalid value"},
		{"missing image", "name: a\n", "image", "is required"},
		{"image with whitespace", "name: a\nimage: \"nginx; rm -rf /\"\n", "image", "whitespace"},
		{"image with uppercase repo", "name: a\nimage: Nginx:latest\n", "image", "not a valid image reference"},
		{"port too large", "name: a\nimage: nginx\nport: 70000\n", "port", "invalid value 70000"},
		{"port zero", "name: a\nimage: nginx\nport: 0\n", "port", "invalid value 0"},
		{"domain without port", "name: a\nimage: nginx\ndomain: a.example.com\n", "port", "required when domain is set"},
		{"domain injection", "name: a\nimage: nginx\nport: 80\ndomain: \"a.com { respond 200 }\"\n", "domain", "not a valid hostname"},
		{"domain with scheme", "name: a\nimage: nginx\nport: 80\ndomain: https://a.com\n", "domain", "not a valid hostname"},
		{"replicas zero", "name: a\nimage: nginx\nreplicas: 0\n", "replicas", "invalid value 0"},
		{"replicas too many", "name: a\nimage: nginx\nreplicas: 500\n", "replicas", "invalid value 500"},
		{"bad memory", "name: a\nimage: nginx\nresources:\n  memory: abc\n", "resources.memory", `invalid value "abc"`},
		{"tiny memory", "name: a\nimage: nginx\nresources:\n  memory: 1mb\n", "resources.memory", "minimum is 6mb"},
		{"bad cpu", "name: a\nimage: nginx\nresources:\n  cpu: lots\n", "resources.cpu", `invalid value "lots"`},
		{"negative cpu", "name: a\nimage: nginx\nresources:\n  cpu: -1\n", "resources.cpu", "must be between"},
		{"bad restart policy", "name: a\nimage: nginx\nrestart:\n  policy: sometimes\n", "restart.policy", `invalid value "sometimes"`},
		{"bad strategy", "name: a\nimage: nginx\ndeploy:\n  strategy: yolo\n", "deploy.strategy", `invalid value "yolo"`},
		{"health without path", "name: a\nimage: nginx\nport: 80\nhealth:\n  interval: 5s\n", "health.path", "is required"},
		{"health without port", "name: a\nimage: nginx\nhealth:\n  path: /up\n", "port", "required when health is set"},
		{"health relative path", "name: a\nimage: nginx\nport: 80\nhealth:\n  path: health\n", "health.path", "must start with /"},
		{"health path with newline", "name: a\nimage: nginx\nport: 80\nhealth:\n  path: \"/up\\r\\nHost: evil\"\n", "health.path", "control characters"},
		{"bad interval", "name: a\nimage: nginx\nport: 80\nhealth:\n  path: /up\n  interval: soon\n", "health.interval", `invalid value "soon"`},
		{"interval out of range", "name: a\nimage: nginx\nport: 80\nhealth:\n  path: /up\n  interval: 1ms\n", "health.interval", "out of range"},
		{"bad retries", "name: a\nimage: nginx\nport: 80\nhealth:\n  path: /up\n  retries: 0\n", "health.retries", "invalid value 0"},
		{"bad env key", "name: a\nimage: nginx\nenv:\n  \"MY-VAR\": x\n", "env.MY-VAR", "invalid variable name"},
		{"unknown field", "name: a\nimage: nginx\nreplicsa: 2\n", "line 3", `unknown field "replicsa"`},
		{"wrong type", "name: a\nimage: nginx\nport: abc\n", "line 3", "a number"},
		{"empty file", "", "deploy.yaml", "file is empty"},
		{"multiple documents", "name: a\nimage: nginx\n---\nname: b\nimage: nginx\n", "deploy.yaml", "multiple YAML documents"},
		{"malformed yaml", "name: [unclosed\n", "deploy.yaml", ""},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.config))
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("expected *ValidationError, got %v", err)
			}
			for _, f := range verr.Fields {
				if f.Field == tt.wantField && strings.Contains(f.Message, tt.wantMsg) {
					return
				}
			}
			t.Errorf("no error for field %q containing %q; got %+v", tt.wantField, tt.wantMsg, verr.Fields)
		})
	}
}

func TestParseReportsAllErrorsAtOnce(t *testing.T) {
	_, err := Parse([]byte("name: Bad_Name\nimage: nginx\nport: 99999\nresources:\n  memory: abc\n"))
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *ValidationError, got %v", err)
	}
	if len(verr.Fields) != 3 {
		t.Errorf("got %d errors, want 3: %+v", len(verr.Fields), verr.Fields)
	}
}

func TestUnknownFieldDoesNotHideOtherErrors(t *testing.T) {
	_, err := Parse([]byte("name: a\nimage: nginx\nport: 99999\nreplicsa: 2\nresources:\n  memory: lots\n"))
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *ValidationError, got %v", err)
	}
	got := map[string]bool{}
	for _, f := range verr.Fields {
		got[f.Field] = true
	}
	for _, want := range []string{"line 4", "port", "resources.memory"} {
		if !got[want] {
			t.Errorf("missing an error for %q; got %+v", want, verr.Fields)
		}
	}
}

func TestTypeErrorIsNotReportedTwice(t *testing.T) {
	// `port: abc` leaves port unset. Reporting "port is required when domain
	// is set" on top of the type error would be misleading.
	_, err := Parse([]byte("name: a\nimage: nginx\nport: abc\ndomain: a.example.com\n"))
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("expected *ValidationError, got %v", err)
	}
	if len(verr.Fields) != 1 || verr.Fields[0].Field != "line 3" {
		t.Errorf("want only the type error on line 3, got %+v", verr.Fields)
	}
}

func TestValidationErrorFormat(t *testing.T) {
	_, err := Parse([]byte("name: a\nimage: nginx\nresources:\n  memory: abc\n"))
	want := "invalid deploy.yaml\n\nresources.memory:\n  invalid value \"abc\"\n  expected: 128mb, 512mb, 1gb, ...\n"
	if err == nil || err.Error() != want {
		t.Errorf("got:\n%v\nwant:\n%s", err, want)
	}
}

func TestValidationErrorNeverEchoesEnvValues(t *testing.T) {
	_, err := Parse([]byte("name: a\nimage: nginx\nenv:\n  SECRET: \"hunter2\\0\"\n"))
	if err == nil {
		t.Fatal("expected an error")
	}
	if strings.Contains(err.Error(), "hunter2") {
		t.Errorf("error leaks env value: %v", err)
	}
}

func TestParseRejectsOversizedConfig(t *testing.T) {
	big := "name: a\nimage: nginx\n#" + strings.Repeat("x", MaxConfigBytes)
	if _, err := Parse([]byte(big)); err == nil {
		t.Error("expected an error for oversized config")
	}
}

func TestVersion(t *testing.T) {
	tests := map[string]string{
		"nginx":                        "latest",
		"nginx:1.27":                   "1.27",
		"ghcr.io/company/my-api:1.4.2": "1.4.2",
		"localhost:5000/app:dev":       "dev",
		"nginx@sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef": "sha256:0123456789ab",
	}
	for image, want := range tests {
		if got := (App{Image: image}).Version(); got != want {
			t.Errorf("Version(%q) = %q, want %q", image, got, want)
		}
	}
}

func TestAppJSONRoundTrip(t *testing.T) {
	app, err := Parse([]byte(fullConfig))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	data, err := json.Marshal(app)
	if err != nil {
		t.Fatalf("Marshal: %v", err)
	}
	if !strings.Contains(string(data), `"interval":"10s"`) {
		t.Errorf("durations should marshal as strings: %s", data)
	}
	var back App
	if err := json.Unmarshal(data, &back); err != nil {
		t.Fatalf("Unmarshal: %v", err)
	}
	if back.Health.Interval != app.Health.Interval || back.Resources != app.Resources || back.Name != app.Name {
		t.Errorf("round trip mismatch: %+v vs %+v", back, app)
	}
}

func TestRedactedMasksEnvValues(t *testing.T) {
	app := App{Name: "a", Env: map[string]string{"SECRET": "hunter2"}}
	red := app.Redacted()
	if red.Env["SECRET"] == "hunter2" {
		t.Error("Redacted must mask env values")
	}
	if _, ok := red.Env["SECRET"]; !ok {
		t.Error("Redacted must keep env names")
	}
	if app.Env["SECRET"] != "hunter2" {
		t.Error("Redacted must not mutate the original")
	}
}
