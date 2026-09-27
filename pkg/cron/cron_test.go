package cron

import (
	"strings"
	"testing"
	"time"
)

func at(s string) time.Time {
	t, err := time.Parse("2006-01-02 15:04", s)
	if err != nil {
		panic(err)
	}
	return t
}

func TestMatches(t *testing.T) {
	tests := []struct {
		expr    string
		matches []string
		misses  []string
	}{
		{"* * * * *", []string{"2026-03-01 00:00", "2026-12-31 23:59"}, nil},
		{"0 3 * * *", []string{"2026-03-01 03:00"}, []string{"2026-03-01 03:01", "2026-03-01 04:00"}},
		{"*/15 * * * *", []string{"2026-03-01 10:00", "2026-03-01 10:15", "2026-03-01 10:45"}, []string{"2026-03-01 10:10"}},
		{"5/20 * * * *", []string{"2026-03-01 10:05", "2026-03-01 10:25", "2026-03-01 10:45"}, []string{"2026-03-01 10:00"}},
		{"0 9-17 * * mon-fri", []string{"2026-03-02 09:00", "2026-03-06 17:00"}, []string{"2026-03-01 09:00", "2026-03-02 18:00"}}, // 2026-03-01 is a Sunday
		{"0 0 1,15 * *", []string{"2026-03-01 00:00", "2026-03-15 00:00"}, []string{"2026-03-02 00:00"}},
		{"0 0 * jan,JUL *", []string{"2026-01-10 00:00", "2026-07-10 00:00"}, []string{"2026-03-10 00:00"}},
		{"0 0 * * 7", []string{"2026-03-01 00:00"}, []string{"2026-03-02 00:00"}},
		{"0 0 * * sun", []string{"2026-03-01 00:00"}, []string{"2026-03-02 00:00"}},
		// Both day fields restricted: either one matching is enough.
		{"0 0 13 * fri", []string{"2026-03-13 00:00", "2026-03-06 00:00", "2026-04-13 00:00"}, []string{"2026-03-12 00:00"}},
		{"0 0 29 2 *", []string{"2028-02-29 00:00"}, []string{"2026-02-28 00:00"}},
		{"0-10/5 1-3 * * *", []string{"2026-03-01 01:00", "2026-03-01 02:05", "2026-03-01 03:10"}, []string{"2026-03-01 01:15", "2026-03-01 04:00"}},
	}
	for _, tt := range tests {
		s, err := Parse(tt.expr)
		if err != nil {
			t.Errorf("Parse(%q): %v", tt.expr, err)
			continue
		}
		for _, m := range tt.matches {
			if !s.Matches(at(m)) {
				t.Errorf("%q should match %s", tt.expr, m)
			}
		}
		for _, m := range tt.misses {
			if s.Matches(at(m)) {
				t.Errorf("%q should not match %s", tt.expr, m)
			}
		}
	}
}

func TestMatchesReadsTheTimeInUTC(t *testing.T) {
	s, _ := Parse("0 3 * * *")
	local := time.FixedZone("east", 2*3600)
	if !s.Matches(time.Date(2026, 3, 1, 5, 0, 0, 0, local)) {
		t.Error("05:00 at UTC+2 is 03:00 UTC and should match")
	}
	if s.Matches(time.Date(2026, 3, 1, 3, 0, 0, 0, local)) {
		t.Error("03:00 at UTC+2 is 01:00 UTC and should not match")
	}
}

func TestNext(t *testing.T) {
	tests := []struct {
		expr  string
		after string
		want  string
	}{
		{"* * * * *", "2026-03-01 10:00", "2026-03-01 10:01"},
		{"0 3 * * *", "2026-03-01 02:59", "2026-03-01 03:00"},
		{"0 3 * * *", "2026-03-01 03:00", "2026-03-02 03:00"},
		{"*/15 * * * *", "2026-03-01 10:01", "2026-03-01 10:15"},
		{"0 9 * * mon-fri", "2026-03-06 09:00", "2026-03-09 09:00"}, // Friday → Monday
		{"0 0 1 * *", "2026-03-01 00:00", "2026-04-01 00:00"},
		{"0 0 31 * *", "2026-04-01 00:00", "2026-05-31 00:00"}, // April has no 31st
		{"0 0 29 2 *", "2026-03-01 00:00", "2028-02-29 00:00"},
		{"30 23 * * *", "2026-12-31 23:31", "2027-01-01 23:30"},
	}
	for _, tt := range tests {
		s, err := Parse(tt.expr)
		if err != nil {
			t.Fatalf("Parse(%q): %v", tt.expr, err)
		}
		got := s.Next(at(tt.after))
		if want := at(tt.want); !got.Equal(want) {
			t.Errorf("%q after %s: Next = %s, want %s", tt.expr, tt.after, got.Format("2006-01-02 15:04"), tt.want)
		}
		if !s.Matches(got) {
			t.Errorf("%q: Next returned %s, which it does not match", tt.expr, got)
		}
	}
}

func TestNextIgnoresSecondsAndZones(t *testing.T) {
	s, _ := Parse("* * * * *")
	local := time.FixedZone("west", -5*3600)
	got := s.Next(time.Date(2026, 3, 1, 10, 0, 30, 0, local))
	if want := time.Date(2026, 3, 1, 15, 1, 0, 0, time.UTC); !got.Equal(want) || got.Location() != time.UTC {
		t.Errorf("Next = %s, want %s in UTC", got, want)
	}
}

func TestParseErrors(t *testing.T) {
	tests := map[string]string{
		"":              "expected 5 fields",
		"0 3 * *":       "expected 5 fields",
		"0 3 * * * *":   "expected 5 fields",
		"60 * * * *":    "minute: value 60 out of range 0-59",
		"* 24 * * *":    "hour: value 24 out of range 0-23",
		"* * 0 * *":     "day of month: value 0 out of range 1-31",
		"* * * 13 *":    "month: value 13 out of range 1-12",
		"* * * * 8":     "day of week: value 8 out of range 0-7",
		"* * * * mon-":  `day of week: invalid value ""`,
		"* * * * ,mon":  "day of week: empty list item",
		"*/0 * * * *":   `minute: invalid step "0"`,
		"*/x * * * *":   `minute: invalid step "x"`,
		"10-5 * * * *":  "minute: invalid range",
		"* * * foo *":   `month: invalid value "foo"`,
		"@daily":        "expected 5 fields",
		"1.5 * * * *":   `minute: invalid value "1.5"`,
		"* * * * mon/2": "", // a step on a single value is "from here on"
	}
	for expr, want := range tests {
		_, err := Parse(expr)
		switch {
		case want == "" && err != nil:
			t.Errorf("Parse(%q) = %v, want success", expr, err)
		case want != "" && err == nil:
			t.Errorf("Parse(%q) succeeded, want error containing %q", expr, want)
		case want != "" && !strings.Contains(err.Error(), want):
			t.Errorf("Parse(%q) = %q, want error containing %q", expr, err, want)
		}
	}
}

func TestStringNormalizesWhitespace(t *testing.T) {
	s, _ := Parse("  0   3 *  * *\t")
	if s.String() != "0 3 * * *" {
		t.Errorf("String = %q", s.String())
	}
}
