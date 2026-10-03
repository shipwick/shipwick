package spec

import (
	"bytes"
	"fmt"
	"strings"
	"time"

	"go.yaml.in/yaml/v3"

	"github.com/shipwick/shipwick/pkg/cron"
)

// Defaults and bounds of scheduled backups.
const (
	DefaultBackupKeep = 7
	MaxBackupKeep     = 365
	// DefaultBackupBeforeTimeout is how long backups.before may run when
	// before_timeout does not say; MaxBackupBeforeTimeout is the most it can
	// say. The application is locked for as long as the command runs.
	DefaultBackupBeforeTimeout = time.Hour
	MaxBackupBeforeTimeout     = 24 * time.Hour
)

// Backups says when an application's volumes are backed up unasked.
type Backups struct {
	Schedule string `json:"schedule"` // five-field cron expression, UTC
	// Keep is how many successful backups are kept; older ones are removed
	// once a new one has succeeded.
	Keep int `json:"keep"`
	// Before is run inside the running replica before the archive is taken: a
	// dump, a checkpoint. A non-zero exit fails the backup.
	Before []string `json:"before,omitempty"`
	// Stop stops the application while the archive is taken, for data that
	// cannot be copied consistently from under a running process.
	Stop bool `json:"stop,omitempty"`
	// BeforeTimeout is how long Before may run before it is stopped and the
	// backup fails. Zero, in a deployment recorded before the key existed,
	// means DefaultBackupBeforeTimeout.
	BeforeTimeout Duration `json:"before_timeout,omitempty"`
}

// BeforeLimit is how long Before may run.
func (b *Backups) BeforeLimit() time.Duration {
	if b.BeforeTimeout > 0 {
		return b.BeforeTimeout.Std()
	}
	return DefaultBackupBeforeTimeout
}

// backupsRaw mirrors the `backups` block.
type backupsRaw struct {
	Schedule string   `yaml:"schedule"`
	Keep     *int     `yaml:"keep"`
	Before   []string `yaml:"before"`
	Stop     bool     `yaml:"stop"`

	BeforeTimeout string `yaml:"before_timeout"`
}

// validateBackups checks the `backups` block.
func (r raw) validateBackups(verr *ValidationError, app App) *Backups {
	if r.Backups.IsZero() {
		return nil
	}
	const example = "backups:\n    schedule: \"0 3 * * *\""
	if r.Backups.Kind != yaml.MappingNode {
		verr.add("backups", "must be a block with a schedule", example)
		return nil
	}
	// Decoded from its own text: a node's Decode accepts keys it does not
	// know, and a typo in `schedule` must not pass for "no schedule".
	text, err := yaml.Marshal(&r.Backups)
	if err != nil {
		verr.add("backups", "could not be read", example)
		return nil
	}
	var b backupsRaw
	dec := yaml.NewDecoder(bytes.NewReader(text))
	dec.KnownFields(true)
	if err := dec.Decode(&b); err != nil {
		// The line numbers count from the block, not from the file, and are
		// left out.
		problems, _ := syntaxError(err)
		for _, f := range problems.Fields {
			verr.add("backups", f.Message, "schedule, keep, before, before_timeout, stop")
		}
		return nil
	}

	out := &Backups{Schedule: strings.Join(strings.Fields(b.Schedule), " "), Keep: DefaultBackupKeep, Before: b.Before, Stop: b.Stop}
	if out.Schedule == "" {
		verr.add("backups.schedule", "is required", `"0 3 * * *" (minute hour day-of-month month day-of-week, in UTC)`)
	} else if _, err := cron.Parse(out.Schedule); err != nil {
		verr.add("backups.schedule", fmt.Sprintf("invalid value %q: %v", b.Schedule, err),
			`five cron fields in UTC, e.g. "0 3 * * *" (every day at 03:00)`)
	}
	if b.Keep != nil {
		if *b.Keep < 1 || *b.Keep > MaxBackupKeep {
			verr.add("backups.keep", fmt.Sprintf("invalid value %d", *b.Keep), fmt.Sprintf("a number between 1 and %d", MaxBackupKeep))
		}
		out.Keep = *b.Keep
	}
	if b.Before != nil {
		if err := ValidateCommand(b.Before); err != nil {
			verr.add("backups.before", err.Error(), `["pg_dump", "-U", "postgres", "-f", "/var/lib/postgresql/data/backup.sql", "app"]`)
		}
		out.BeforeTimeout = Duration(DefaultBackupBeforeTimeout)
	}
	if v := b.BeforeTimeout; v != "" {
		const example = "10m, 1h, 6h, ... (1s to 24h)"
		if b.Before == nil {
			verr.add("backups.before_timeout", "needs backups.before: it is that command's time limit", example)
		} else if d, err := parseDuration(v, time.Second, MaxBackupBeforeTimeout); err != nil {
			verr.add("backups.before_timeout", err.Error(), example)
		} else {
			out.BeforeTimeout = Duration(d)
		}
	}
	switch {
	case r.Static.Dir != "":
		verr.add("backups", staticExclusiveMessage, "")
	case len(r.Volumes) == 0:
		verr.add("backups", "needs volumes: a backup is an archive of the application's volumes", "volumes:\n    - name: data\n      path: /var/lib/postgresql/data")
	}
	return out
}
