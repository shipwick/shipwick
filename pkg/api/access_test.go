package api

import (
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestALimitedTokenAllowsOnlyItsApplications(t *testing.T) {
	limited := TokenIdentity{Role: RoleDeploy, Applications: []string{"api", "web"}}
	if !limited.Limited() || !limited.Allows("api") || !limited.Allows("web") || limited.Allows("worker") || limited.Allows("") {
		t.Errorf("limited token: Limited = %v", limited.Limited())
	}
	for _, whole := range []TokenIdentity{
		{Role: RoleDeploy},
		{Role: RoleDeploy, Applications: []string{}},
		{Role: RoleAdmin, Applications: []string{"api"}},
	} {
		if whole.Limited() || !whole.Allows("worker") {
			t.Errorf("%+v must not be limited", whole)
		}
	}
}

func TestTokenApplicationsAreValidatedSortedAndCountedOnce(t *testing.T) {
	got, err := ValidateTokenApplications(RoleDeploy, []string{"web", "api", "web"})
	if err != nil || !reflect.DeepEqual(got, []string{"api", "web"}) {
		t.Errorf("got %v, %v", got, err)
	}
	for _, role := range []Role{RoleRead, RoleDeploy, RoleAdmin} {
		if got, err := ValidateTokenApplications(role, nil); err != nil || got == nil || len(got) != 0 {
			t.Errorf("%s without applications: %v, %v; want an empty list", role, got, err)
		}
	}
	for _, role := range []Role{RoleRead, RoleAdmin} {
		if _, err := ValidateTokenApplications(role, []string{"api"}); err == nil || !strings.HasPrefix(err.Error(), "only a deploy token can be limited") {
			t.Errorf("%s with applications: %v", role, err)
		}
	}
	if _, err := ValidateTokenApplications(RoleDeploy, []string{"Not Valid"}); err == nil || !strings.HasPrefix(err.Error(), "applications: ") {
		t.Errorf("an invalid name: %v", err)
	}
}

func TestParseExpiry(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 30, 15, 500, time.UTC)
	for in, want := range map[string]time.Time{
		"90d":                       time.Date(2027, 1, 1, 9, 30, 15, 0, time.UTC),
		"12h":                       time.Date(2026, 10, 3, 21, 30, 15, 0, time.UTC),
		"1h":                        time.Date(2026, 10, 3, 10, 30, 15, 0, time.UTC),
		"2026-10-03":                time.Date(2026, 10, 4, 0, 0, 0, 0, time.UTC),
		"2027-01-31":                time.Date(2027, 2, 1, 0, 0, 0, 0, time.UTC),
		"2026-12-24T18:00:00+02:00": time.Date(2026, 12, 24, 16, 0, 0, 0, time.UTC),
	} {
		got, err := ParseExpiry(in, now)
		if err != nil || !got.Equal(want) || got.Location() != time.UTC {
			t.Errorf("ParseExpiry(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	for in, message := range map[string]string{
		"":                     `invalid expiry "": use days or hours from now, or a date, e.g. 90d, 12h or 2027-01-03`,
		"90":                   "invalid expiry",
		"0d":                   "invalid expiry",
		"-3d":                  "invalid expiry",
		"+3d":                  "invalid expiry",
		"1.5d":                 "invalid expiry",
		"3w":                   "invalid expiry",
		"30m":                  "invalid expiry",
		"99999999999d":         "invalid expiry",
		"4000d":                "invalid expiry",
		"tomorrow":             "invalid expiry",
		"2026-10-02":           "the expiry is in the past",
		"2026-10-03T09:30:15Z": "the expiry is in the past",
	} {
		if _, err := ParseExpiry(in, now); err == nil || !strings.HasPrefix(err.Error(), message) {
			t.Errorf("ParseExpiry(%q): %v; want %q", in, err, message)
		}
	}
}

func TestParseSince(t *testing.T) {
	now := time.Date(2026, 10, 3, 9, 30, 0, 0, time.UTC)
	for in, want := range map[string]time.Time{
		"7d":                   time.Date(2026, 9, 26, 9, 30, 0, 0, time.UTC),
		"24h":                  time.Date(2026, 10, 2, 9, 30, 0, 0, time.UTC),
		"2026-09-01":           time.Date(2026, 9, 1, 0, 0, 0, 0, time.UTC),
		"2026-09-01T10:00:00Z": time.Date(2026, 9, 1, 10, 0, 0, 0, time.UTC),
	} {
		if got, err := ParseSince(in, now); err != nil || !got.Equal(want) {
			t.Errorf("ParseSince(%q) = %s, %v; want %s", in, got, err, want)
		}
	}
	for _, in := range []string{"", "7", "yesterday", "90m", "0h"} {
		if _, err := ParseSince(in, now); err == nil || !strings.HasPrefix(err.Error(), "invalid time") {
			t.Errorf("ParseSince(%q): %v", in, err)
		}
	}
}
