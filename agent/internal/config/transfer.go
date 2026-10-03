package config

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/shipwick/shipwick/pkg/cron"
)

const (
	EnvExportSchedule  = "SHIPWICK_EXPORT_SCHEDULE"
	EnvExportKeep      = "SHIPWICK_EXPORT_KEEP"
	EnvStandbySchedule = "SHIPWICK_STANDBY_SCHEDULE"
)

// Transfer says what the agent does on a schedule for a second server, or as
// one.
type Transfer struct {
	// ExportSchedule is when an export of the whole server is written to
	// where backups go; nil: only on request. ExportKeep is how many are kept.
	ExportSchedule *cron.Schedule
	ExportKeep     int
	// StandbySchedule makes this server a standby: when it fetches the newest
	// export from the bucket and imports it with every application stopped.
	StandbySchedule *cron.Schedule
}

func (c *Config) loadTransfer(getenv func(string) string) error {
	var err error
	if c.Transfer.ExportSchedule, err = schedule(getenv, EnvExportSchedule); err != nil {
		return err
	}
	if c.Transfer.StandbySchedule, err = schedule(getenv, EnvStandbySchedule); err != nil {
		return err
	}
	c.Transfer.ExportKeep = 3
	if v := strings.TrimSpace(getenv(EnvExportKeep)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > 100 {
			return fmt.Errorf("%s must be a number from 1 to 100", EnvExportKeep)
		}
		c.Transfer.ExportKeep = n
	}

	switch {
	case c.Transfer.ExportSchedule != nil && c.Transfer.StandbySchedule != nil:
		return fmt.Errorf("%s and %s are both set: a server writes exports or stands by for one that does", EnvExportSchedule, EnvStandbySchedule)
	case c.Transfer.ExportSchedule != nil && c.Backups.Passphrase == "":
		// An export holds every secret of the server in clear; without a
		// passphrase there is nothing to write it with.
		return fmt.Errorf("%s needs %s: an export is only ever written encrypted", EnvExportSchedule, EnvBackupPassphrase)
	case c.Transfer.StandbySchedule != nil && (c.Backups.S3 == nil || c.Backups.Passphrase == ""):
		return fmt.Errorf("%s needs the bucket and the passphrase of the server this one stands by for: set the %s* variables and %s to the values that server has",
			EnvStandbySchedule, EnvBackupS3Endpoint[:len("SHIPWICK_BACKUP_S3_")], EnvBackupPassphrase)
	}
	return nil
}

func schedule(getenv func(string) string, name string) (*cron.Schedule, error) {
	expr := strings.TrimSpace(getenv(name))
	if expr == "" {
		return nil, nil
	}
	s, err := cron.Parse(expr)
	if err != nil {
		return nil, fmt.Errorf("%s: %w (five fields, UTC, for example \"0 4 * * *\")", name, err)
	}
	return &s, nil
}
