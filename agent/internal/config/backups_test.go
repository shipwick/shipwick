package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestBackupsDefaultToADirectoryUnderTheDataDirectory(t *testing.T) {
	cfg, err := Load(env(map[string]string{EnvDataDir: "/data"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Backups.Dir != filepath.Join("/data", "backups") || cfg.Backups.Passphrase != "" || cfg.Backups.S3 != nil {
		t.Fatalf("unexpected defaults: %+v", cfg.Backups)
	}
}

func TestBackupsConfiguration(t *testing.T) {
	cfg, err := Load(env(map[string]string{
		EnvBackupDir:               "/mnt/backups",
		EnvBackupPassphrase:        "correct horse battery staple",
		EnvBackupS3Endpoint:        "https://account.r2.cloudflarestorage.com",
		EnvBackupS3Bucket:          "shipwick",
		EnvBackupS3AccessKeyID:     "key-id",
		EnvBackupS3SecretAccessKey: "secret-key",
		EnvBackupS3Prefix:          "alpha",
	}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	b := cfg.Backups
	if b.Dir != "/mnt/backups" || b.Passphrase != "correct horse battery staple" || b.S3 == nil {
		t.Fatalf("unexpected backups: %+v", b)
	}
	if b.S3.Region != "auto" || b.S3.Bucket != "shipwick" || b.S3.Prefix != "alpha" {
		t.Fatalf("unexpected bucket: region %q, bucket %q, prefix %q", b.S3.Region, b.S3.Bucket, b.S3.Prefix)
	}
}

func TestBackupsConfigurationIsRefusedWithoutRepeatingASecret(t *testing.T) {
	bucket := map[string]string{
		EnvBackupS3Endpoint:        "https://s3.example.com",
		EnvBackupS3Bucket:          "shipwick",
		EnvBackupS3AccessKeyID:     "the-key-id",
		EnvBackupS3SecretAccessKey: "the-secret-key",
	}
	with := func(change map[string]string) map[string]string {
		out := map[string]string{}
		for k, v := range bucket {
			out[k] = v
		}
		for k, v := range change {
			out[k] = v
		}
		return out
	}
	tests := map[string]struct {
		vars map[string]string
		want string
	}{
		"short passphrase": {map[string]string{EnvBackupPassphrase: "hunter2"}, "at least 12 characters"},
		"bucket alone":     {map[string]string{EnvBackupS3Bucket: "shipwick"}, EnvBackupS3Endpoint},
		"no secret":        {with(map[string]string{EnvBackupS3SecretAccessKey: ""}), EnvBackupS3SecretAccessKey + " is not"},
		"endpoint no url":  {with(map[string]string{EnvBackupS3Endpoint: "s3.example.com"}), "must be a URL"},
		"embedded secrets": {with(map[string]string{EnvBackupS3Endpoint: "https://id:hunter2pass@s3.example.com"}), "without credentials"},
	}
	for name, tc := range tests {
		t.Run(name, func(t *testing.T) {
			_, err := Load(env(tc.vars))
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("got %v, want an error containing %q", err, tc.want)
			}
			for _, secret := range []string{"hunter2", "the-secret-key", "the-key-id"} {
				if strings.Contains(err.Error(), secret) {
					t.Fatalf("the error repeats a secret: %v", err)
				}
			}
		})
	}
}
