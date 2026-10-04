package api

import (
	"reflect"
	"strings"
	"testing"
)

func TestACellThatWouldBeAFormulaIsWrittenAsText(t *testing.T) {
	for value, want := range map[string]string{
		"":                      "",
		"ci":                    "ci",
		"ada@example.com":       "ada@example.com",
		"deployment 12":         "deployment 12",
		"a=b":                   "a=b",
		"=1+1":                  "'=1+1",
		"+1+1":                  "'+1+1",
		"-2+3":                  "'-2+3",
		"@SUM(A1)":              "'@SUM(A1)",
		"=cmd|' /C calc'!A0":    "'=cmd|' /C calc'!A0",
		"\t=1+1":                "'\t=1+1",
		"\r=1+1":                "'\r=1+1",
		"-AAAA_bbbb":            "'-AAAA_bbbb",
		"'=1+1":                 "'=1+1",
		"applications a -> all": "applications a -> all",
	} {
		if got := SpreadsheetSafe(value); got != want {
			t.Errorf("SpreadsheetSafe(%q) = %q, want %q", value, got, want)
		}
	}
}

func TestAnAuditFilterTakesActionsFamiliesAndOutcomes(t *testing.T) {
	for _, action := range []string{"deploy", "token.create", "token.", "application.delete", "signin"} {
		if err := ValidateAuditAction(action); err != nil {
			t.Errorf("ValidateAuditAction(%q) = %v", action, err)
		}
	}
	for _, action := range []string{"", ".", ".create", "token..", "token.*", "Token.create", "a.b.c", "token create", "token.create.", "%", strings.Repeat("a", 21)} {
		if err := ValidateAuditAction(action); err == nil {
			t.Errorf("ValidateAuditAction(%q) accepted it", action)
		}
	}
	for _, outcome := range []string{AuditOK, AuditRefused, AuditFailed} {
		if err := ValidateAuditOutcome(outcome); err != nil {
			t.Errorf("ValidateAuditOutcome(%q) = %v", outcome, err)
		}
	}
	if err := ValidateAuditOutcome("denied"); err == nil || err.Error() != `invalid outcome "denied": use ok, refused or failed` {
		t.Errorf("an outcome that is none: %v", err)
	}
	if ValidateActorKind(ActorToken) != nil || ValidateActorKind(ActorUser) != nil || ValidateActorKind("person") == nil {
		t.Error("the kinds of actor are token and user")
	}
	if got := SplitList([]string{"token., deploy", "", "deploy,,backup.", " "}); !reflect.DeepEqual(got, []string{"token.", "deploy", "backup."}) {
		t.Errorf("SplitList = %q", got)
	}
	if got := SplitList(nil); got == nil || len(got) != 0 {
		t.Errorf("SplitList(nil) = %#v, want an empty list", got)
	}
}

func TestAPersonIsNamedByAnAddressInLowercaseOrByTheClaimAsItIs(t *testing.T) {
	for _, tt := range []struct{ claim, value, want string }{
		{"email", " Ada@Example.com ", "ada@example.com"},
		{"", "Ada@Example.com", "ada@example.com"},
		{"upn", "Ada@Example.com", "Ada@Example.com"},
		{"preferred_username", "ada", "ada"},
		{"sub", "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ", "AAAAAAAAAAAAAAAAAAAAAIkzqFVrSaSaFHy782bbtaQ"},
		{"sub", "-_leading-dash-and-underscore", "-_leading-dash-and-underscore"},
		{"sub", "auth0|5f7c8ec7c33c6c004bbafe82", "auth0|5f7c8ec7c33c6c004bbafe82"},
		{"sub", "248289761001", "248289761001"},
	} {
		if got, err := PersonName(tt.claim, tt.value); err != nil || got != tt.want {
			t.Errorf("PersonName(%q, %q) = %q, %v; want %q", tt.claim, tt.value, got, err, tt.want)
		}
	}
	for _, tt := range []struct{ claim, value string }{
		{"email", "ada"},
		{"email", ""},
		{"sub", ""},
		{"sub", "ada lovelace"},
		{"sub", " ada"},
		{"sub", "ada\n"},
		{"sub", "ada\x00"},
		{"sub", "a/b"},
		{"sub", `a\b`},
		{"sub", `a"b`},
		{"sub", "a,b"},
		{"sub", "a;b"},
		{"sub", "ädä"},
		{"preferred_username", strings.Repeat("a", 255)},
	} {
		if got, err := PersonName(tt.claim, tt.value); err == nil {
			t.Errorf("PersonName(%q, %q) = %q, want it refused", tt.claim, tt.value, got)
		}
	}
	for name, want := range map[string]string{"ada@example.com": "ada@example.com", "Ada@Example.com": "name:Ada@Example.com", "ada": "name:ada", "248289761001": "name:248289761001"} {
		if got := WhoOf(name); got != want {
			t.Errorf("WhoOf(%q) = %q, want %q", name, got, want)
		}
		// What WhoOf writes reads back as a rule for exactly that person.
		kind, subject, err := ParseAccessSubject(WhoOf(name))
		if grant, ok := ResolveAccess([]AccessRule{{Kind: kind, Subject: subject, Role: RoleRead}}, name, nil); err != nil || !ok || grant.Role != RoleRead {
			t.Errorf("the rule %q does not match %q (%v)", WhoOf(name), name, err)
		}
	}
}

func TestRulesAreMatchedAgainstANameThatIsNotAlwaysAnAddress(t *testing.T) {
	rules := []AccessRule{
		{Kind: AccessDomain, Subject: "corp.example", Role: RoleRead},
		{Kind: AccessEmail, Subject: "grace@corp.example", Role: RoleDeploy},
		{Kind: AccessName, Subject: "Grace@Corp.Example", Role: RoleAdmin},
		{Kind: AccessName, Subject: "svc-deploy", Role: RoleDeploy, Applications: []string{"web"}},
		{Kind: AccessGroup, Subject: "platform", Role: RoleAdmin},
		{Kind: AccessEmail, Subject: "svc-backup", Role: RoleAdmin}, // not an address: matches nobody
	}
	for _, tt := range []struct {
		why, name string
		groups    []string
		want      Role
		ok        bool
	}{
		{"the name, character for character, before the address", "Grace@Corp.Example", nil, RoleAdmin, true},
		{"the address in any case, where the name rule does not match", "GRACE@corp.example", nil, RoleDeploy, true},
		{"the address before the group", "grace@corp.example", []string{"platform"}, RoleDeploy, true},
		{"the domain for a name that is an address there", "Ada@CORP.example", nil, RoleRead, true},
		{"a name that is not an address, by name", "svc-deploy", nil, RoleDeploy, true},
		{"another capital is another name", "Svc-deploy", nil, "", false},
		{"a name that is not an address gets nothing from a domain", "corp.example", nil, "", false},
		{"nor from an address rule that is not an address", "svc-backup", nil, "", false},
		{"a group for a name that is not an address", "248289761001", []string{"platform"}, RoleAdmin, true},
		{"an at sign does not make an address", "ada@", nil, "", false},
		{"nor a domain in front of one", "@corp.example", nil, "", false},
		{"nor two", "ada@evil.example@corp.example", nil, "", false},
	} {
		got, ok := ResolveAccess(rules, tt.name, tt.groups)
		if ok != tt.ok || got.Role != tt.want {
			t.Errorf("%s: %q gets %+v, %v; want %s, %v", tt.why, tt.name, got, ok, tt.want, tt.ok)
		}
	}

	for who, want := range map[string][2]string{
		"name:248289761001": {AccessName, "248289761001"},
		"name:Ada.Lovelace": {AccessName, "Ada.Lovelace"},
		"name:ada@x.org":    {AccessName, "ada@x.org"},
		"Ada@Example.com":   {AccessEmail, "ada@example.com"},
	} {
		kind, subject, err := ParseAccessSubject(who)
		if err != nil || kind != want[0] || subject != want[1] {
			t.Errorf("ParseAccessSubject(%q) = %s %q, %v", who, kind, subject, err)
		}
		if got := (AccessRule{Kind: kind, Subject: subject}).Who(); kind == AccessName && got != who {
			t.Errorf("Who() = %q, want %q", got, who)
		}
	}
	for _, who := range []string{"name:", "name:ada lovelace", "ada"} {
		if _, _, err := ParseAccessSubject(who); err == nil {
			t.Errorf("ParseAccessSubject(%q) accepted it", who)
		}
	}
}
