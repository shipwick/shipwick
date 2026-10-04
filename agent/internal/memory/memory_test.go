package memory

import (
	"strings"
	"testing"
)

func TestSwapTotal(t *testing.T) {
	for _, tc := range []struct {
		name    string
		meminfo string
		want    int64
		ok      bool
	}{
		{"a server without swap", "MemTotal:        3906068 kB\nSwapCached:            0 kB\nSwapTotal:             0 kB\nSwapFree:              0 kB\n", 0, true},
		{"a server with swap", "MemTotal:        3906068 kB\nSwapTotal:       2097148 kB\nSwapFree:        2097148 kB\n", 2097148 * 1024, true},
		{"no such line", "MemTotal:        3906068 kB\n", 0, false},
		{"another unit", "SwapTotal:       2 MB\n", 0, false},
		{"not a number", "SwapTotal:       many kB\n", 0, false},
		{"nothing", "", 0, false},
	} {
		got, ok := swapTotal(strings.NewReader(tc.meminfo))
		if got != tc.want || ok != tc.ok {
			t.Errorf("%s: swapTotal = %d, %v; want %d, %v", tc.name, got, ok, tc.want, tc.ok)
		}
	}
}
