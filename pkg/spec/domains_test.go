package spec

import (
	"errors"
	"fmt"
	"strings"
	"testing"
)

func TestParseAliasesAndRedirects(t *testing.T) {
	app, err := Parse([]byte("name: web\nimage: nginx:1.27\nport: 80\ndomain: Example.com\n" +
		"aliases: [\" API.example.com \"]\nredirects:\n  - www.example.com\n  - EXAMPLE.net\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := strings.Join(app.Aliases, ","); got != "api.example.com" {
		t.Errorf("Aliases = %q, want them normalized like domain", got)
	}
	if got := strings.Join(app.Redirects, ","); got != "www.example.com,example.net" {
		t.Errorf("Redirects = %q, want them normalized like domain, in the order given", got)
	}

	plain, err := Parse([]byte("name: web\nimage: nginx:1.27\nport: 80\ndomain: example.com\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if plain.Aliases != nil || plain.Redirects != nil {
		t.Errorf("Aliases = %v, Redirects = %v; want nil so that the JSON form omits them", plain.Aliases, plain.Redirects)
	}

	if _, err := Parse([]byte("name: web\nimage: nginx:1.27\nport: 80\ndomain: example.com\nredirects: [" + hosts(MaxHostnames) + "]\n")); err != nil {
		t.Errorf("%d redirects should be accepted: %v", MaxHostnames, err)
	}
}

func hosts(n int) string {
	out := make([]string, n)
	for i := range out {
		out[i] = fmt.Sprintf("h%d.example.com", i)
	}
	return strings.Join(out, ", ")
}

func TestHostnameValidationErrors(t *testing.T) {
	const base = "name: web\nimage: nginx:1.27\nport: 80\n"
	const served = base + "domain: example.com\n"
	tests := []struct {
		name      string
		config    string
		wantField string
		wantMsg   string
	}{
		{"aliases without domain", base + "aliases: [api.example.com]\n", "aliases", "requires domain"},
		{"redirects without domain", base + "redirects: [www.example.com]\n", "redirects", "requires domain"},
		{"alias with a path", served + "aliases: [\"api.example.com/v1\"]\n", "aliases[0]", "not a valid hostname"},
		{"redirect with a scheme", served + "redirects: [\"https://www.example.com\"]\n", "redirects[0]", "not a valid hostname"},
		{"empty redirect", served + "redirects: [\"\"]\n", "redirects[0]", "is empty"},
		{"alias repeats the domain", served + "aliases: [EXAMPLE.com]\n", "aliases[0]", "already listed under domain"},
		{"alias listed twice", served + "aliases: [a.example.com, a.example.com]\n", "aliases[1]", "already listed under aliases[0]"},
		{"redirect that is also an alias", served + "aliases: [a.example.com]\nredirects: [www.example.com, a.example.com]\n", "redirects[1]", "already listed under aliases[0]"},
		{"too many aliases", served + "aliases: [" + hosts(MaxHostnames+1) + "]\n", "aliases", fmt.Sprintf("too many (%d)", MaxHostnames+1)},
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
