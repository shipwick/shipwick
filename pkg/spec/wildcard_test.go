package spec

import (
	"errors"
	"strings"
	"testing"
)

func TestValidateHostnameAcceptsOneLeadingWildcardLabel(t *testing.T) {
	for _, h := range []string{"example.com", "api.example.com", "*.example.com", "*.apps.example.com"} {
		if err := ValidateHostname(h); err != nil {
			t.Errorf("ValidateHostname(%q) = %v, want it accepted", h, err)
		}
	}
	for _, h := range []string{"*", "*.", "*example.com", "a*.example.com", "api.*.example.com", "*.*.example.com", "**.example.com", "*.example.com/x", "*.-bad.com"} {
		if err := ValidateHostname(h); err == nil {
			t.Errorf("ValidateHostname(%q) accepted it", h)
		}
	}
	if !IsWildcard("*.example.com") || IsWildcard("example.com") {
		t.Error("IsWildcard does not tell *.example.com from example.com")
	}
}

func TestParseAcceptsAWildcardAsDomainAndAsAlias(t *testing.T) {
	app, err := Parse([]byte("name: web\nimage: nginx:1.27\nport: 80\ndomain: \"*.Example.com\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if app.Domain != "*.example.com" {
		t.Errorf("Domain = %q", app.Domain)
	}

	app, err = Parse([]byte("name: web\nimage: nginx:1.27\nport: 80\ndomain: example.com\naliases: [\"*.example.com\"]\nredirects: [www.example.net]\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := strings.Join(app.Aliases, ","); got != "*.example.com" {
		t.Errorf("Aliases = %q", got)
	}
}

func TestParseRefusesWildcardsWhereOneNameIsNeeded(t *testing.T) {
	cases := []struct{ name, yaml, field, message string }{
		{"a redirect that is a wildcard",
			"domain: example.com\nredirects: [\"*.example.com\"]\n", "redirects[0]", "not a valid hostname"},
		{"redirects to a wildcard domain",
			"domain: \"*.example.com\"\nredirects: [www.example.net]\n", "redirects", "cannot be sent to a wildcard domain"},
		{"a wildcard in the middle",
			"domain: api.*.example.com\n", "domain", "a wildcard is one leading label and nothing else"},
		{"two wildcard labels",
			"domain: example.com\naliases: [\"*.*.example.com\"]\n", "aliases[0]", "a wildcard is one leading label and nothing else"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			_, err := Parse([]byte("name: web\nimage: nginx:1.27\nport: 80\n" + tc.yaml))
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("Parse = %v, want a validation error", err)
			}
			for _, f := range verr.Fields {
				if f.Field == tc.field && strings.Contains(f.Message, tc.message) {
					return
				}
			}
			t.Errorf("no error on %s containing %q: %+v", tc.field, tc.message, verr.Fields)
		})
	}
}
