package api

import (
	"fmt"
	"strings"
	"testing"
)

func TestAnOversizedSecretIsRefusedWithTheLimitAndNotItsSize(t *testing.T) {
	err := ValidateSecretValue(strings.Repeat("x", 3*MaxSecretValueBytes))
	if err == nil {
		t.Fatal("expected an error")
	}
	// Nothing computed from the value: the limit is the one number said.
	limit, size := fmt.Sprintf("max %d KB", MaxSecretValueBytes/1024), fmt.Sprintf("%d KB", 3*MaxSecretValueBytes/1024)
	if msg := err.Error(); strings.Count(msg, "KB") != 1 || !strings.Contains(msg, limit) || strings.Contains(msg, size) {
		t.Errorf("the message should say the limit and nothing of the value: %q", msg)
	}
}
