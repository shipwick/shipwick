package spec

import (
	"errors"
	"strings"
	"testing"
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
		"missing schedule": {"backups:\n  keep: 3\n", "backups.schedule", "is required"},
		"bad schedule":     {"backups:\n  schedule: daily\n", "backups.schedule", `invalid value "daily"`},
		"keep zero":        {"backups:\n  schedule: '0 3 * * *'\n  keep: 0\n", "backups.keep", "invalid value 0"},
		"keep too many":    {"backups:\n  schedule: '0 3 * * *'\n  keep: 366\n", "backups.keep", "invalid value 366"},
		"empty before":     {"backups:\n  schedule: '0 3 * * *'\n  before: []\n", "backups.before", "is required"},
		"unknown key":      {"backups:\n  schedule: '0 3 * * *'\n  retain: 3\n", "backups", `unknown field "retain"`},
		"keep not number":  {"backups:\n  schedule: '0 3 * * *'\n  keep: many\n", "backups", "a number"},
		"not a block":      {"backups: nightly\n", "backups", "must be a block"},
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
