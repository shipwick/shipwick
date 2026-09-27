package api

import "testing"

func TestRoleCoversTheRolesBelowIt(t *testing.T) {
	tests := []struct {
		have, need Role
		want       bool
	}{
		{RoleRead, RoleRead, true},
		{RoleRead, RoleDeploy, false},
		{RoleRead, RoleAdmin, false},
		{RoleDeploy, RoleRead, true},
		{RoleDeploy, RoleDeploy, true},
		{RoleDeploy, RoleAdmin, false},
		{RoleAdmin, RoleRead, true},
		{RoleAdmin, RoleDeploy, true},
		{RoleAdmin, RoleAdmin, true},
		{Role("owner"), RoleRead, false}, // unknown roles cover nothing
	}
	for _, tt := range tests {
		if got := tt.have.Covers(tt.need); got != tt.want {
			t.Errorf("%s covers %s = %v, want %v", tt.have, tt.need, got, tt.want)
		}
	}
	if ValidateRole(RoleDeploy) != nil || ValidateRole(Role("")) == nil || ValidateRole(Role("Admin")) == nil {
		t.Error("ValidateRole should accept exactly read, deploy and admin")
	}
}

func TestTokenNameRules(t *testing.T) {
	valid := []string{"ci", "a", "0", "github-actions", "deploy-bot-2", "abcdefghijabcdefghijabcdefghijabcdefghij"}
	for _, name := range valid {
		if err := ValidateTokenName(name); err != nil {
			t.Errorf("%q should be valid: %v", name, err)
		}
	}
	invalid := []string{"", "root", "CI", "-ci", "ci-", "my ci", "ci_bot", "ci.bot", "abcdefghijabcdefghijabcdefghijabcdefghijk", "../etc"}
	for _, name := range invalid {
		if err := ValidateTokenName(name); err == nil {
			t.Errorf("%q should be rejected", name)
		}
	}
}
