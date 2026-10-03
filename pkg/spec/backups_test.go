package spec

import (
	"errors"
	"strings"
	"testing"
	"time"
)

const backupsBase = `
name: db
image: postgres:17
volumes:
  - name: data
    path: /var/lib/postgresql/data
deploy:
  strategy: recreate
`

func TestParseBackups(t *testing.T) {
	app, err := Parse([]byte(backupsBase + `
backups:
  schedule: " 0  3 * * *"
  keep: 14
  before: ["pg_dump", "-U", "postgres", "-f", "/var/lib/postgresql/data/backup.sql", "app"]
  stop: true
`))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	b := app.Backups
	if b == nil || b.Schedule != "0 3 * * *" || b.Keep != 14 || len(b.Before) != 6 || !b.Stop {
		t.Fatalf("unexpected backups: %+v", b)
	}
	if b.BeforeTimeout.Std() != DefaultBackupBeforeTimeout || b.BeforeLimit() != time.Hour {
		t.Fatalf("before has %s without before_timeout, want an hour", b.BeforeTimeout)
	}
}

func TestBackupsBeforeTimeout(t *testing.T) {
	const before = "backups:\n  schedule: '0 3 * * *'\n  before: [pg_dump, app]\n"
	app, err := Parse([]byte(backupsBase + before + "  before_timeout: 2h30m\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := app.Backups.BeforeLimit(); got != 150*time.Minute {
		t.Fatalf("before_timeout: 2h30m gives the command %s", got)
	}
	for _, ok := range []string{"1s", "24h"} {
		if _, err := Parse([]byte(backupsBase + before + "  before_timeout: " + ok + "\n")); err != nil {
			t.Errorf("before_timeout: %s was refused: %v", ok, err)
		}
	}
	// A deployment recorded before the key existed has no value, and the
	// hour it always had.
	if got := (&Backups{Before: []string{"dump"}}).BeforeLimit(); got != time.Hour {
		t.Errorf("without a value the command has %s, want an hour", got)
	}
}

func TestBackupsDefaults(t *testing.T) {
	app, err := Parse([]byte(backupsBase + "backups:\n  schedule: '0 3 * * *'\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if b := app.Backups; b == nil || b.Keep != DefaultBackupKeep || b.Before != nil || b.Stop {
		t.Fatalf("unexpected defaults: %+v", b)
	}
	app, err = Parse([]byte(backupsBase))
	if err != nil || app.Backups != nil {
		t.Fatalf("a file without backups has none: %+v, %v", app.Backups, err)
	}
}

func TestBackupsValidation(t *testing.T) {
	tests := map[string]struct {
		yaml  string
		field string
		msg   string
	}{
		"missing schedule":  {"backups:\n  keep: 3\n", "backups.schedule", "is required"},
		"bad schedule":      {"backups:\n  schedule: daily\n", "backups.schedule", `invalid value "daily"`},
		"keep zero":         {"backups:\n  schedule: '0 3 * * *'\n  keep: 0\n", "backups.keep", "invalid value 0"},
		"keep too many":     {"backups:\n  schedule: '0 3 * * *'\n  keep: 366\n", "backups.keep", "invalid value 366"},
		"timeout too long":  {"backups:\n  schedule: '0 3 * * *'\n  before: [dump]\n  before_timeout: 25h\n", "backups.before_timeout", "out of range"},
		"timeout too short": {"backups:\n  schedule: '0 3 * * *'\n  before: [dump]\n  before_timeout: 500ms\n", "backups.before_timeout", "out of range"},
		"timeout no unit":   {"backups:\n  schedule: '0 3 * * *'\n  before: [dump]\n  before_timeout: 90\n", "backups.before_timeout", `invalid value "90"`},
		"timeout alone":     {"backups:\n  schedule: '0 3 * * *'\n  before_timeout: 2h\n", "backups.before_timeout", "needs backups.before"},
		"empty before":      {"backups:\n  schedule: '0 3 * * *'\n  before: []\n", "backups.before", "is required"},
		"unknown key":       {"backups:\n  schedule: '0 3 * * *'\n  retain: 3\n", "backups", `unknown field "retain"`},
		"keep not number":   {"backups:\n  schedule: '0 3 * * *'\n  keep: many\n", "backups", "a number"},
		"not a block":       {"backups: nightly\n", "backups", "must be a block"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Parse([]byte(backupsBase + tc.yaml))
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("got %v, want a validation error", err)
			}
			for _, f := range verr.Fields {
				if f.Field == tc.field && strings.Contains(f.Message, tc.msg) {
					return
				}
			}
			t.Fatalf("no error for %s containing %q in:\n%v", tc.field, tc.msg, verr)
		})
	}
}

func TestBackupsNeedVolumes(t *testing.T) {
	_, err := Parse([]byte("name: api\nimage: nginx:1.27\nbackups:\n  schedule: '0 3 * * *'\n"))
	var verr *ValidationError
	if !errors.As(err, &verr) || len(verr.Fields) != 1 || verr.Fields[0].Field != "backups" || !strings.Contains(verr.Fields[0].Message, "needs volumes") {
		t.Fatalf("got %v, want one error about volumes", err)
	}
}

func TestBackupsDoNotApplyToStaticApplications(t *testing.T) {
	_, err := Parse([]byte("name: site\nstatic: dist/\ndomain: example.com\nbackups:\n  schedule: '0 3 * * *'\n"))
	var verr *ValidationError
	if !errors.As(err, &verr) || len(verr.Fields) != 1 || verr.Fields[0].Field != "backups" {
		t.Fatalf("got %v, want one error for backups", err)
	}
}
