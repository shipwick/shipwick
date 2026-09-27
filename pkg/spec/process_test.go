package spec

import (
	"errors"
	"strings"
	"testing"
)

func TestProcessOverrides(t *testing.T) {
	app, err := Parse([]byte("name: api\nimage: app:1\nentrypoint: [\"dotnet\"]\ncommand: [\"App.dll\", \"--urls\", \"http://0.0.0.0:8080\"]\nuser: \"1000:1000\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if strings.Join(app.Entrypoint, "|") != "dotnet" || strings.Join(app.Command, "|") != "App.dll|--urls|http://0.0.0.0:8080" || app.User != "1000:1000" {
		t.Errorf("app = %+v", app)
	}

	// Omitted keys keep the image's own; a null is the same as omitted.
	app, err = Parse([]byte("name: api\nimage: app:1\nentrypoint:\n"))
	if err != nil || app.Entrypoint != nil || app.Command != nil || app.User != "" {
		t.Errorf("omitted: %+v, %v", app, err)
	}

	// A string is one argument, spaces and all: there is no shell to split it.
	app, err = Parse([]byte("name: api\nimage: app:1\ncommand: serve --port 8080\n"))
	if err != nil || len(app.Command) != 1 || app.Command[0] != "serve --port 8080" {
		t.Errorf("scalar command: %+v, %v", app.Command, err)
	}

	for _, user := range []string{"app", "1000", "1000:1000", "1000:app", "app:app", "_svc-1", " 33 "} {
		app, err := Parse([]byte("name: api\nimage: app:1\nuser: \"" + user + "\"\n"))
		if err != nil || app.User != strings.TrimSpace(user) {
			t.Errorf("user %q: %q, %v", user, app.User, err)
		}
	}
	if app, err := Parse([]byte("name: api\nimage: app:1\nuser: 1000\n")); err != nil || app.User != "1000" {
		t.Errorf("a bare number is a uid: %q, %v", app.User, err)
	}

	for _, tt := range []struct{ name, config, field, msg string }{
		{"empty entrypoint", "entrypoint: []\n", "entrypoint", "must not be empty"},
		{"empty command", "command: []\n", "command", "must not be empty"},
		{"too many arguments", "command: [" + strings.Repeat("a,", 64) + "a]\n", "command", "too many arguments (65)"},
		{"newline in an argument", "command: [\"a\\nb\"]\n", "command[0]", "newlines"},
		{"NUL in an argument", "entrypoint: [\"sh\", \"a\\0b\"]\n", "entrypoint[1]", "NUL"},
		{"a map is neither", "entrypoint: {a: b}\n", "line 3", "a string or a list of strings, not a map"},
		{"user with a shell in it", "user: \"root; rm -rf /\"\n", "user", "invalid value"},
		{"uppercase user", "user: App\n", "user", "invalid value"},
		{"group without user", "user: \":1000\"\n", "user", "invalid value"},
		{"uid too long", "user: \"12345678901\"\n", "user", "invalid value"},
		{"name too long", "user: " + strings.Repeat("a", 33) + "\n", "user", "invalid value"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte("name: api\nimage: app:1\n" + tt.config))
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v", err)
			}
			for _, f := range verr.Fields {
				if f.Field == tt.field && strings.Contains(f.Message, tt.msg) {
					return
				}
			}
			t.Errorf("no error for %q containing %q; got %+v", tt.field, tt.msg, verr.Fields)
		})
	}
}
