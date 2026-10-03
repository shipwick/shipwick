package api

import (
	"reflect"
	"strings"
	"testing"
)

func TestTheMostSpecificRuleDecidesWhatAPersonMayDo(t *testing.T) {
	rules := []AccessRule{
		{Kind: AccessDomain, Subject: "example.com", Role: RoleRead},
		{Kind: AccessGroup, Subject: "developers", Role: RoleDeploy, Applications: []string{"web"}},
		{Kind: AccessGroup, Subject: "backend", Role: RoleDeploy, Applications: []string{"my-api", "web"}},
		{Kind: AccessGroup, Subject: "release", Role: RoleDeploy},
		{Kind: AccessGroup, Subject: "platform", Role: RoleAdmin},
		{Kind: AccessGroup, Subject: "auditors", Role: RoleRead},
		{Kind: AccessEmail, Subject: "contractor@example.com", Role: RoleRead},
		{Kind: AccessEmail, Subject: "ada@elsewhere.org", Role: RoleAdmin},
	}
	for _, tt := range []struct {
		why    string
		email  string
		groups []string
		want   Grant
		ok     bool
	}{
		{"the domain, where nothing closer matches", "grace@example.com", nil, Grant{RoleRead, []string{}}, true},
		{"a group over the domain", "grace@example.com", []string{"developers"}, Grant{RoleDeploy, []string{"web"}}, true},
		{"the address over a group, even to give less", "contractor@example.com", []string{"platform"}, Grant{RoleRead, []string{}}, true},
		{"the address where the domain has no rule", "ada@elsewhere.org", nil, Grant{RoleAdmin, []string{}}, true},
		{"the highest role of several groups", "grace@example.com", []string{"auditors", "platform", "developers"}, Grant{RoleAdmin, []string{}}, true},
		{"limits of the same role add up", "grace@example.com", []string{"developers", "backend"}, Grant{RoleDeploy, []string{"my-api", "web"}}, true},
		{"a group without a limit lifts it", "grace@example.com", []string{"backend", "release"}, Grant{RoleDeploy, []string{}}, true},
		{"a lower role adds nothing", "grace@example.com", []string{"developers", "auditors"}, Grant{RoleDeploy, []string{"web"}}, true},
		{"a group the rules do not name", "grace@elsewhere.org", []string{"sales"}, Grant{}, false},
		{"nobody without a rule", "mallory@elsewhere.org", nil, Grant{}, false},
		{"a subdomain is another domain", "grace@mail.example.com", nil, Grant{}, false},
	} {
		got, ok := ResolveAccess(rules, tt.email, tt.groups)
		if ok != tt.ok || ok && !got.Equal(tt.want) {
			t.Errorf("%s: ResolveAccess(%s, %v) = %+v, %v; want %+v, %v", tt.why, tt.email, tt.groups, got, ok, tt.want, tt.ok)
		}
	}
	// What is resolved must not be the rule's own slice: sessions keep it.
	got, _ := ResolveAccess(rules, "grace@example.com", []string{"developers", "backend"})
	if !reflect.DeepEqual(rules[1].Applications, []string{"web"}) || len(got.Applications) != 2 {
		t.Errorf("resolving changed a rule: %+v", rules[1])
	}
}

func TestWhoARuleIsForIsReadTheWayItIsWritten(t *testing.T) {
	for in, want := range map[string][2]string{
		"Ada@Example.com":     {AccessEmail, "ada@example.com"},
		"*@Example.com":       {AccessDomain, "example.com"},
		"group:Platform Team": {AccessGroup, "Platform Team"},
		"group:/admins":       {AccessGroup, "/admins"},
		"group:a@b":           {AccessGroup, "a@b"},
	} {
		kind, subject, err := ParseAccessSubject(in)
		if err != nil || kind != want[0] || subject != want[1] {
			t.Errorf("ParseAccessSubject(%q) = %q, %q, %v; want %q, %q", in, kind, subject, err, want[0], want[1])
			continue
		}
		if back, _, _ := ParseAccessSubject(AccessRule{Kind: kind, Subject: subject}.Who()); back != kind {
			t.Errorf("%q does not survive being printed: %q", in, AccessRule{Kind: kind, Subject: subject}.Who())
		}
	}
	for _, in := range []string{"", "ada", "ada@", "@example.com", "a b@example.com", "ada@exa mple.com", "ada@example.com\nx", "*@", "*@*.example.com",
		"group:", "group: padded", "group:line\nbreak", "ada@" + strings.Repeat("a", 250) + ".com", "<script>@example.com"} {
		if kind, subject, err := ParseAccessSubject(in); err == nil {
			t.Errorf("ParseAccessSubject(%q) = %q, %q; want an error", in, kind, subject)
		}
	}
	if _, err := ValidateAccessSubject("everyone", "x"); err == nil {
		t.Error("an unknown kind was accepted")
	}
}
