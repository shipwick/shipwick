package api

import (
	"fmt"
	"regexp"
	"strings"
)

// Limits on the secrets stored on the server. A value is an environment
// variable's worth of text: a password, a key, a certificate at most.
const (
	MaxSecretNameLength = 64
	MaxSecretValueBytes = 64 * 1024
	MaxSecrets          = 500
)

// A secret is used as ${NAME}, so its name is an environment variable name.
var secretNamePattern = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// ValidateSecretName checks the name of a secret for PUT and DELETE /secrets/{name}.
func ValidateSecretName(name string) error {
	if len(name) > MaxSecretNameLength || !secretNamePattern.MatchString(name) {
		return fmt.Errorf("invalid secret name %q: use letters, digits and underscores (max %d characters), e.g. DATABASE_PASSWORD", name, MaxSecretNameLength)
	}
	return nil
}

// ValidateSecretValue checks a value for PUT /secrets/{name}. An empty value
// is refused: an empty password is the worse surprise.
func ValidateSecretValue(value string) error {
	switch {
	case value == "":
		return fmt.Errorf("the value is empty")
	case len(value) > MaxSecretValueBytes:
		return fmt.Errorf("the value is too large (%d KB, max %d KB)", len(value)/1024, MaxSecretValueBytes/1024)
	case strings.ContainsRune(value, 0):
		return fmt.Errorf("the value must not contain NUL bytes")
	}
	return nil
}
