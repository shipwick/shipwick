package api

import (
	"strings"
	"testing"
)

func TestValidateSecretName(t *testing.T) {
	for _, name := range []string{"DATABASE_PASSWORD", "_x", "a1", strings.Repeat("A", 64)} {
		if err := ValidateSecretName(name); err != nil {
			t.Errorf("%q: %v", name, err)
		}
	}
	for _, name := range []string{"", "1ABC", "db-password", "DB PASSWORD", "${X}", strings.Repeat("A", 65)} {
		if err := ValidateSecretName(name); err == nil {
			t.Errorf("%q: expected an error", name)
		}
	}
}

func TestValidateSecretValue(t *testing.T) {
	if err := ValidateSecretValue(strings.Repeat("x", MaxSecretValueBytes)); err != nil {
		t.Errorf("a value at the limit: %v", err)
	}
	for _, value := range []string{"", "a\x00b", strings.Repeat("x", MaxSecretValueBytes+1)} {
		err := ValidateSecretValue(value)
		if err == nil {
			t.Errorf("%q: expected an error", value[:min(len(value), 8)])
		} else if strings.Contains(err.Error(), "xxxx") {
			t.Errorf("the error echoes the value: %v", err)
		}
	}
}
