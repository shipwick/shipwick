package docker

import (
	"strings"
	"testing"
)

func TestTailBufferKeepsTheEnd(t *testing.T) {
	tail := &tailBuffer{limit: 8}
	for _, chunk := range []string{"abc", "def", "ghij", strings.Repeat("x", 20), "yz"} {
		if n, err := tail.Write([]byte(chunk)); n != len(chunk) || err != nil {
			t.Fatalf("Write(%q) = %d, %v", chunk, n, err)
		}
	}
	if got := tail.String(); got != "xxxxxxyz" {
		t.Errorf("tail = %q, want the last 8 bytes written", got)
	}

	tail = &tailBuffer{limit: 8}
	tail.Write([]byte("short"))
	if got := tail.String(); got != "short" {
		t.Errorf("tail = %q; under the limit nothing is dropped", got)
	}
}
