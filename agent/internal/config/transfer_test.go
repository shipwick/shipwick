package config

import (
	"strings"
	"testing"
)

var standbyBucket = map[string]string{
	EnvBackupPassphrase:        "correct horse battery staple",
	EnvBackupS3Endpoint:        "https://account.r2.cloudflarestorage.com",
	EnvBackupS3Bucket:          "shipwick",
	EnvBackupS3AccessKeyID:     "key-id",
	EnvBackupS3SecretAccessKey: "secret-key",
}

func with(base map[string]string, extra map[string]string) map[string]string {
	out := map[string]string{}
	for k, v := range base {
		out[k] = v
	}
	for k, v := range extra {
		out[k] = v
	}
	return out
}

func TestNoExportOrStandbyScheduleByDefault(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Transfer.ExportSchedule != nil || cfg.Transfer.StandbySchedule != nil || cfg.Transfer.ExportKeep != 3 {
		t.Fatalf("unexpected defaults: %+v", cfg.Transfer)
	}
}

func TestExportScheduleNeedsAPassphrase(t *testing.T) {
	_, err := Load(env(map[string]string{EnvExportSchedule: "0 4 * * *"}))
	if err == nil || !strings.Contains(err.Error(), EnvBackupPassphrase) {
		t.Fatalf("a schedule without a passphrase: %v", err)
	}
	cfg, err := Load(env(map[string]string{EnvExportSchedule: "0 4 * * *", EnvExportKeep: "5", EnvBackupPassphrase: "correct horse battery staple"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Transfer.ExportSchedule == nil || cfg.Transfer.ExportSchedule.String() != "0 4 * * *" || cfg.Transfer.ExportKeep != 5 {
		t.Fatalf("unexpected transfer: %+v", cfg.Transfer)
	}
}

func TestStandbyScheduleNeedsTheBucketAndThePassphrase(t *testing.T) {
	_, err := Load(env(map[string]string{EnvStandbySchedule: "*/15 * * * *", EnvBackupPassphrase: "correct horse battery staple"}))
	if err == nil || !strings.Contains(err.Error(), "SHIPWICK_BACKUP_S3_") {
		t.Fatalf("a standby without a bucket: %v", err)
	}
	cfg, err := Load(env(with(standbyBucket, map[string]string{EnvStandbySchedule: "*/15 * * * *"})))
	if err != nil || cfg.Transfer.StandbySchedule == nil {
		t.Fatalf("a standby with a bucket: %+v, %v", cfg.Transfer, err)
	}
}

func TestAServerIsNotBothAtOnce(t *testing.T) {
	_, err := Load(env(with(standbyBucket, map[string]string{EnvStandbySchedule: "*/15 * * * *", EnvExportSchedule: "0 4 * * *"})))
	if err == nil || !strings.Contains(err.Error(), "both set") {
		t.Fatalf("both schedules: %v", err)
	}
}

func TestSchedulesAndKeepAreValidated(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		EnvExportSchedule:  {EnvExportSchedule: "every day", EnvBackupPassphrase: "correct horse battery staple"},
		EnvStandbySchedule: with(standbyBucket, map[string]string{EnvStandbySchedule: "61 * * * *"}),
		EnvExportKeep:      {EnvExportKeep: "0"},
	} {
		if _, err := Load(env(vars)); err == nil || !strings.Contains(err.Error(), name) {
			t.Errorf("%s: %v", name, err)
		}
	}
}
