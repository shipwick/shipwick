package main

import (
	"fmt"
	"log/slog"

	"github.com/shipwick/shipwick/agent/internal/backup"
	"github.com/shipwick/shipwick/agent/internal/config"
	"github.com/shipwick/shipwick/agent/internal/deploy"
)

// backupOptions turns the configuration into where backups go, and says so in
// the log: once, at startup, without a secret in it.
func backupOptions(cfg config.Config, encryptionKey []byte, log *slog.Logger) (deploy.BackupOptions, error) {
	var bucket *backup.S3
	if cfg.Backups.S3 != nil {
		var err error
		if bucket, err = backup.NewS3(*cfg.Backups.S3); err != nil {
			return deploy.BackupOptions{}, fmt.Errorf("%s: %w", config.EnvBackupS3Endpoint, err)
		}
		// A standby reads from the bucket and writes nothing to it: transfer.go
		// says so in its own line.
		if cfg.Transfer.StandbySchedule == nil {
			log.Info("backups go to a bucket as well as to the server", "dir", cfg.Backups.Dir, "host", bucket.Host(), "bucket", bucket.Bucket())
		}
	} else {
		log.Info("backups stay on the server: they survive a bad deployment, not the loss of the server", "dir", cfg.Backups.Dir,
			"set", config.EnvBackupS3Endpoint+" and the other "+config.EnvBackupS3Bucket[:len("SHIPWICK_BACKUP_S3_")]+"* variables")
	}
	if cfg.Backups.Passphrase == "" {
		log.Warn("the agent's own state is not backed up: the encryption key exists only on this server, and losing it loses every secret",
			"set", config.EnvBackupPassphrase)
	}
	return deploy.BackupOptions{
		Storage:       backup.New(cfg.Backups.Dir, bucket, cfg.Backups.Passphrase),
		EncryptionKey: encryptionKey,
	}, nil
}
