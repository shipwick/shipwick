package config

import (
	"fmt"
	"path/filepath"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/backup"
)

const (
	EnvBackupDir        = "SHIPWICK_BACKUP_DIR"
	EnvBackupPassphrase = "SHIPWICK_BACKUP_PASSPHRASE"

	EnvBackupS3Endpoint        = "SHIPWICK_BACKUP_S3_ENDPOINT"
	EnvBackupS3Bucket          = "SHIPWICK_BACKUP_S3_BUCKET"
	EnvBackupS3AccessKeyID     = "SHIPWICK_BACKUP_S3_ACCESS_KEY_ID"
	EnvBackupS3SecretAccessKey = "SHIPWICK_BACKUP_S3_SECRET_ACCESS_KEY"
	EnvBackupS3Region          = "SHIPWICK_BACKUP_S3_REGION"
	EnvBackupS3Prefix          = "SHIPWICK_BACKUP_S3_PREFIX"
)

// MinBackupPassphraseLength rejects passphrases that a key derivation cannot
// make up for.
const MinBackupPassphraseLength = 12

// Backups says where the backups the agent takes are kept.
type Backups struct {
	// Dir is the directory on the server; <data dir>/backups unless
	// SHIPWICK_BACKUP_DIR says otherwise.
	Dir string
	// Passphrase, when set, encrypts every backup, and is what allows the
	// agent's own state to be backed up at all. It is a secret and must never
	// be logged.
	Passphrase string
	// S3 is the bucket backups are also sent to; nil when none is configured.
	// Its two keys are credentials and must never be logged.
	S3 *backup.S3Config
}

func (c *Config) loadBackups(getenv func(string) string) error {
	c.Backups.Dir = valueOr(strings.TrimSpace(getenv(EnvBackupDir)), filepath.Join(c.DataDir, "backups"))
	// The value is a secret: the error names the rule, not the value.
	c.Backups.Passphrase = getenv(EnvBackupPassphrase)
	if p := c.Backups.Passphrase; p != "" && len(p) < MinBackupPassphraseLength {
		return fmt.Errorf("%s must be at least %d characters long", EnvBackupPassphrase, MinBackupPassphraseLength)
	}

	s3 := backup.S3Config{
		Endpoint:        strings.TrimSpace(getenv(EnvBackupS3Endpoint)),
		Bucket:          strings.TrimSpace(getenv(EnvBackupS3Bucket)),
		AccessKeyID:     strings.TrimSpace(getenv(EnvBackupS3AccessKeyID)),
		SecretAccessKey: strings.TrimSpace(getenv(EnvBackupS3SecretAccessKey)),
		Region:          valueOr(strings.TrimSpace(getenv(EnvBackupS3Region)), "auto"),
		Prefix:          strings.TrimSpace(getenv(EnvBackupS3Prefix)),
	}
	required := map[string]string{
		EnvBackupS3Endpoint:        s3.Endpoint,
		EnvBackupS3Bucket:          s3.Bucket,
		EnvBackupS3AccessKeyID:     s3.AccessKeyID,
		EnvBackupS3SecretAccessKey: s3.SecretAccessKey,
	}
	var set, missing []string
	for _, name := range []string{EnvBackupS3Endpoint, EnvBackupS3Bucket, EnvBackupS3AccessKeyID, EnvBackupS3SecretAccessKey} {
		if required[name] == "" {
			missing = append(missing, name)
		} else {
			set = append(set, name)
		}
	}
	switch {
	case len(set) == 0:
		return nil
	case len(missing) > 0:
		return fmt.Errorf("%s is set but %s is not: a bucket needs all four", set[0], strings.Join(missing, ", "))
	}
	// Validated here, so that a mistake stops the agent at startup rather
	// than failing the first backup at three in the morning. The errors never
	// repeat a value.
	if _, err := backup.NewS3(s3); err != nil {
		return fmt.Errorf("%s: %w", EnvBackupS3Endpoint, err)
	}
	c.Backups.S3 = &s3
	return nil
}
