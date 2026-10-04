package commands

import (
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestAccessGrantTakesANameAndSaysWhatARuleCannotMatch(t *testing.T) {
	a := newAccessAgent(t)
	a.nameClaim = "sub"

	out, _, err := a.run(t.TempDir(), "access", "grant", "name:AAAA-bbbb_cccc", "--role", "read")
	if err != nil || a.grantBody != `{"kind":"name","subject":"AAAA-bbbb_cccc","role":"read"}` {
		t.Fatalf("access grant name: %v, sent %s\n%s", err, a.grantBody, out)
	}
	if strings.Contains(out, "This agent names people") {
		t.Errorf("a rule by name on an agent that names people by sub needs no note:\n%s", out)
	}

	// The fake answers every grant with the same rule; what matters here
	// is the note, which is about the rule that came back.
	for _, tt := range []struct {
		claim string
		rule  api.AccessRule
		want  string
	}{
		{"sub", api.AccessRule{Kind: api.AccessEmail, Subject: "ada@example.com"}, "This agent names people by the sub claim: the rule applies to those whose sub is an address. For anyone else use name:<sub>."},
		{"upn", api.AccessRule{Kind: api.AccessDomain, Subject: "example.com"}, "This agent names people by the upn claim"},
		{"email", api.AccessRule{Kind: api.AccessName, Subject: "ada"}, "This agent names people by their e-mail address"},
		{"email", api.AccessRule{Kind: api.AccessEmail, Subject: "ada@example.com"}, ""},
		{"sub", api.AccessRule{Kind: api.AccessGroup, Subject: "platform"}, ""},
		{"sub", api.AccessRule{Kind: api.AccessName, Subject: "ada"}, ""},
		// An agent from before says nothing about the claim: it is email.
		{"", api.AccessRule{Kind: api.AccessEmail, Subject: "ada@example.com"}, ""},
	} {
		got := ruleNote(tt.rule, api.SignInStatus{Configured: true, NameClaim: tt.claim})
		if tt.want == "" && got != "" || !strings.HasPrefix(got, tt.want) {
			t.Errorf("claim %q, rule %s: note = %q, want %q", tt.claim, tt.rule.Who(), got, tt.want)
		}
	}
	if got := ruleNote(api.AccessRule{Kind: api.AccessEmail, Subject: "ada@example.com"}, api.SignInStatus{NameClaim: "sub"}); got != "" {
		t.Errorf("without sign-in there is nothing to note: %q", got)
	}

	for want, args := range map[string][]string{
		"not a name the provider's claim can hold": {"access", "grant", "name:ada lovelace", "--role", "read"},
	} {
		if _, _, err := a.run(t.TempDir(), args...); err == nil || !strings.HasPrefix(err.Error(), want) {
			t.Errorf("%v: err = %v, want %q", args, err, want)
		}
	}
}

func TestAccessSignoutSendsANameAsItWasTypedWhereNamesAreNotAddresses(t *testing.T) {
	a := newAccessAgent(t)
	a.sessions = []api.Session{{ID: 1, Email: "Ada@Corp.example", Role: api.RoleRead}}

	// Named by address: lowercase, as the agent keeps it.
	a.run(t.TempDir(), "access", "signout", "Ada@Corp.example")
	// Named by another claim: capitals tell people apart, and stay.
	a.nameClaim = "upn"
	out, _, err := a.run(t.TempDir(), "access", "signout", "Ada@Corp.example")
	if err != nil || len(a.signedOut) != 2 || a.signedOut[0] != "ada@corp.example" || a.signedOut[1] != "Ada@Corp.example" || !strings.Contains(out, "Signed Ada@Corp.example out of 1 session") {
		t.Fatalf("access signout: %v, sent %q\n%s", err, a.signedOut, out)
	}
	a.run(t.TempDir(), "access", "signout", "name:AAAA-bbbb_cccc")
	if a.signedOut[2] != "AAAA-bbbb_cccc" {
		t.Errorf("a name written as a rule's subject: sent %q", a.signedOut)
	}

	a.signedOut = nil
	for want, args := range map[string][]string{
		"*@corp.example is not a person":                        {"access", "signout", "*@corp.example"},
		`"ada lovelace" is not a name the provider's claim can`: {"access", "signout", "ada lovelace"},
	} {
		if _, _, err := a.run(t.TempDir(), args...); err == nil || !strings.HasPrefix(err.Error(), want) || len(a.signedOut) != 0 {
			t.Errorf("%v: err = %v, sent %q; want %q", args, err, a.signedOut, want)
		}
	}
	a.nameClaim = ""
	if _, _, err := a.run(t.TempDir(), "access", "signout", "ada"); err == nil || !strings.Contains(err.Error(), "not an e-mail address") {
		t.Errorf("a name on an agent that names people by address: %v", err)
	}
}
