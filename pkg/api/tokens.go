package api

import (
	"fmt"
	"regexp"
	"slices"
)

// Roles in ascending order: each one includes the ones before it.
var roleOrder = []Role{RoleRead, RoleDeploy, RoleAdmin}

// Covers reports whether a token with role r may use an endpoint that
// requires `required`.
func (r Role) Covers(required Role) bool {
	return slices.Index(roleOrder, r) >= slices.Index(roleOrder, required)
}

// Valid reports whether r is one of the defined roles.
func (r Role) Valid() bool {
	return slices.Contains(roleOrder, r)
}

// ValidateRole rejects anything but the three roles.
func ValidateRole(r Role) error {
	if !r.Valid() {
		return fmt.Errorf("invalid role %q: use read, deploy or admin", string(r))
	}
	return nil
}

// TokenPrefix starts every token the agent issues. It makes a token
// recognisable where it must not appear — a log, a repository — and is not
// part of the secret.
const TokenPrefix = "swk_"

// Token names are as strict as application names, and shorter: they end up
// in event messages and tables.
var tokenNamePattern = regexp.MustCompile(`^[a-z0-9]([a-z0-9-]{0,38}[a-z0-9])?$`)

// ValidateTokenName checks a name for POST /tokens. The root token's name is
// taken: it is the token the agent was configured with.
func ValidateTokenName(name string) error {
	if !tokenNamePattern.MatchString(name) {
		return fmt.Errorf("invalid token name %q: use lowercase letters, digits and dashes (max 40 characters), e.g. ci", name)
	}
	if name == RootTokenName {
		return fmt.Errorf("%q is the name of the token the agent is configured with; choose another", name)
	}
	return nil
}
