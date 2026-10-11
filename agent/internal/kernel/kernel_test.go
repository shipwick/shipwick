package kernel

import (
	"strings"
	"testing"
)

func TestParseEntriesReadsWhatTheKernelWrites(t *testing.T) {
	for _, tc := range []struct {
		name string
		file string
		want int
		ok   bool
	}{
		{"a stock kernel", "1024\n", 1024, true},
		{"a raised limit", "8192\n", 8192, true},
		{"without the newline", "4096", 4096, true},
		{"not a number", "many\n", 0, false},
		{"zero", "0\n", 0, false},
		{"nothing", "", 0, false},
	} {
		got, ok := parseEntries(strings.NewReader(tc.file))
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: parseEntries = %d, %v; want %d, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
