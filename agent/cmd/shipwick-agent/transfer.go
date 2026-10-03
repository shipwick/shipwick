package main

import (
	"fmt"
	"log/slog"
	"path/filepath"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/agent/internal/config"
	"github.com/shipwick/shipwick/agent/internal/deploy"
)

// transferOptions turns the configuration into the two schedules. A standby
// reads the bucket of the server it stands by for and must write nothing
// there, so its own backups are taken off the bucket: they stay on its disk.
func transferOptions(cfg config.Config, backups *deploy.BackupOptions, log *slog.Logger) (deploy.TransferOptions, error) {
	opts := deploy.TransferOptions{ExportSchedule: cfg.Transfer.ExportSchedule, ExportsKept: cfg.Transfer.ExportKeep}
	if s := cfg.Transfer.ExportSchedule; s != nil {
		log.Info("an export of the server is written on a schedule, to where backups go", "schedule", s.String(), "kept", cfg.Transfer.ExportKeep)
	}
	if s := cfg.Transfer.StandbySchedule; s != nil {
		bucket, err := backup.NewS3(*cfg.Backups.S3)
		if err != nil {
			return deploy.TransferOptions{}, fmt.Errorf("%s: %w", config.EnvBackupS3Endpoint, err)
		}
		// A directory of its own, which nothing writes to: what a standby
		// imports comes from the bucket.
		opts.StandbySource = backup.New(filepath.Join(cfg.DataDir, "standby"), bucket, cfg.Backups.Passphrase)
		opts.StandbySchedule = s
		backups.Storage = backup.New(cfg.Backups.Dir, nil, cfg.Backups.Passphrase)
		log.Info("this server is a standby: it imports the newest export from the bucket on a schedule, with every application stopped, and keeps its own backups on its disk",
			"schedule", s.String(), "host", bucket.Host(), "bucket", bucket.Bucket())
	}
	return opts, nil
}
