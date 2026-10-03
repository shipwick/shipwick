package spec

import (
	"errors"
	"strings"
	"testing"
)

const staticConfig = "name: web\nstatic: dist/\ndomain: example.com\nredirects: [www.example.com]\n"

func TestParseStaticApplication(t *testing.T) {
	app, err := Parse([]byte(staticConfig))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if app.Static == nil || app.Static.Dir != "dist" {
		t.Fatalf("Static = %+v, want the cleaned folder", app.Static)
	}
	if app.Image != "" || app.Port != 0 || app.Replicas != 1 {
		t.Errorf("a static application has no image or port: %+v", app)
	}
	if app.Domain != "example.com" || len(app.Redirects) != 1 {
		t.Errorf("domain and redirects apply to a static application too: %+v", app)
	}
}

func TestStaticDirIsCleaned(t *testing.T) {
	for in, want := range map[string]string{
		"dist/":         "dist",
		"./build":       "build",
		"out/public/":   "out/public",
		`dist\assets\`:  "dist/assets",
		".":             ".",
		"  dist  ":      "dist",
		"a/../dist/./x": "dist/x",
	} {
		app, err := Parse([]byte("name: web\nstatic: \"" + strings.ReplaceAll(in, `\`, `\\`) + "\"\ndomain: example.com\n"))
		if err != nil {
			t.Errorf("%q: %v", in, err)
			continue
		}
		if app.Static.Dir != want {
			t.Errorf("%q: Dir = %q, want %q", in, app.Static.Dir, want)
		}
	}
}

func TestStaticDirMustStayInsideTheProject(t *testing.T) {
	for _, dir := range []string{"..", "../dist", "dist/../..", "/var/www", "C:/sites/dist", " "} {
		_, err := Parse([]byte("name: web\nstatic: \"" + dir + "\"\ndomain: example.com\n"))
		var verr *ValidationError
		if !errors.As(err, &verr) {
			t.Errorf("%q: err = %v, want a validation error", dir, err)
			continue
		}
		if len(verr.Fields) != 1 || verr.Fields[0].Field != "static" {
			t.Errorf("%q: fields = %+v, want one error on static", dir, verr.Fields)
		}
	}
}

func TestStaticApplicationNeedsADomain(t *testing.T) {
	_, err := Parse([]byte("name: web\nstatic: dist\n"))
	var verr *ValidationError
	if !errors.As(err, &verr) || len(verr.Fields) != 1 || verr.Fields[0].Field != "domain" {
		t.Fatalf("err = %v, want exactly one error, on domain", err)
	}
	if !strings.Contains(verr.Fields[0].Message, "static application") {
		t.Errorf("message = %q, want the reason", verr.Fields[0].Message)
	}
}

func TestStaticExcludesEverythingThatDescribesAContainer(t *testing.T) {
	tests := map[string]string{
		"image":      "image: nginx:1.27\n",
		"build":      "build: .\n",
		"port":       "port: 8080\n",
		"replicas":   "replicas: 2\n",
		"env":        "env:\n  A: b\n",
		"health":     "health:\n  tcp: 80\n",
		"resources":  "resources:\n  memory: 128mb\n",
		"volumes":    "volumes:\n  - name: data\n    path: /data\n",
		"publish":    "publish:\n  - port: 5432\n",
		"entrypoint": "entrypoint: [sh]\n",
		"command":    "command: [serve]\n",
		"user":       "user: app\n",
		"logging":    "logging:\n  driver: local\n",
		"pre_deploy": "pre_deploy:\n  command: [migrate]\n",
		"jobs":       "jobs:\n  - name: nightly\n    schedule: \"0 3 * * *\"\n    command: [report]\n",
	}
	for field, yaml := range tests {
		t.Run(field, func(t *testing.T) {
			_, err := Parse([]byte(staticConfig + yaml))
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v, want a validation error", err)
			}
			found := false
			for _, f := range verr.Fields {
				if f.Field == field && f.Message == staticExclusiveMessage {
					found = true
				}
			}
			if !found {
				t.Errorf("fields = %+v, want %s: %q", verr.Fields, field, staticExclusiveMessage)
			}
		})
	}

	// The default replica count is not a choice, and must pass.
	if _, err := Parse([]byte(staticConfig + "replicas: 1\n")); err != nil {
		t.Errorf("replicas: 1 is the default and must be accepted: %v", err)
	}
}

func TestStaticApplicationInJSON(t *testing.T) {
	app, err := Parse([]byte(`{"name": "web", "static": "dist", "domain": "example.com"}`))
	if err != nil || app.Static == nil {
		t.Fatalf("Parse = %+v, %v", app, err)
	}
}

func TestStaticFallback(t *testing.T) {
	app, err := Parse([]byte("name: web\nstatic:\n  dir: dist/\n  fallback: index.html\ndomain: example.com\n"))
	if err != nil || app.Static.Dir != "dist" || app.Static.Fallback != "index.html" {
		t.Fatalf("Static = %+v, %v", app.Static, err)
	}
	if app, err := Parse([]byte("name: web\nstatic: {dir: dist, fallback: app/200.html}\ndomain: example.com\n")); err != nil || app.Static.Fallback != "app/200.html" {
		t.Errorf("a file in a subfolder: %+v, %v", app.Static, err)
	}
	if app, err := Parse([]byte("name: web\nstatic: {dir: dist}\ndomain: example.com\n")); err != nil || app.Static.Fallback != "" {
		t.Errorf("no fallback: %+v, %v", app.Static, err)
	}

	for _, tt := range []struct{ name, static, field, msg string }{
		{"absolute", "{dir: dist, fallback: /index.html}", "static.fallback", "relative to the folder"},
		{"outside the folder", "{dir: dist, fallback: ../index.html}", "static.fallback", "inside the folder"},
		{"a folder", "{dir: dist, fallback: app/}", "static.fallback", "inside the folder"},
		{"not clean", "{dir: dist, fallback: ./index.html}", "static.fallback", "inside the folder"},
		{"a placeholder of the proxy", "{dir: dist, fallback: \"{env.HOME}\"}", "static.fallback", "inside the folder"},
		{"without a folder", "{fallback: index.html}", "static.dir", "is required"},
		{"misspelt key", "{dir: dist, fallbck: index.html}", "line 2", `unknown field "fallbck"`},
	} {
		t.Run(tt.name, func(t *testing.T) {
			wantFieldError(t, "name: web\nstatic: "+tt.static+"\ndomain: example.com\n", tt.field, tt.msg)
		})
	}
}
