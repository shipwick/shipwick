package config

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestLogArchiveDefaultsToTwoWeeksAndOneGigabyteUnderTheDataDirectory(t *testing.T) {
	cfg, err := Load(env(map[string]string{EnvDataDir: "/srv/shipwick"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	want := LogArchive{Dir: filepath.Join("/srv/shipwick", "logs"), RetentionDays: 14, MaxBytes: 1 << 30}
	if cfg.Logs != want {
		t.Errorf("Logs = %+v, want %+v", cfg.Logs, want)
	}
}

func TestLogRetentionIsReadFromTheEnvironment(t *testing.T) {
	cfg, err := Load(env(map[string]string{EnvLogRetentionDays: "30", EnvLogRetentionSize: "250mb"}))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.Logs.RetentionDays != 30 || cfg.Logs.MaxBytes != 250<<20 {
		t.Errorf("Logs = %+v", cfg.Logs)
	}

	cfg, err = Load(env(map[string]string{EnvLogRetentionSize: "0"}))
	if err != nil || cfg.Logs.MaxBytes != 0 {
		t.Errorf("a size of 0 keeps nothing: %+v, %v", cfg.Logs, err)
	}
}

func TestLogRetentionRefusesWhatItCannotRead(t *testing.T) {
	for name, vars := range map[string]map[string]string{
		"no days":       {EnvLogRetentionDays: "0"},
		"too many days": {EnvLogRetentionDays: "366"},
		"not a number":  {EnvLogRetentionDays: "two weeks"},
		"no unit":       {EnvLogRetentionSize: "500"},
		"not a size":    {EnvLogRetentionSize: "plenty"},
	} {
		_, err := Load(env(vars))
		if err == nil || !strings.Contains(err.Error(), "SHIPWICK_LOG_RETENTION_") {
			t.Errorf("%s: err = %v, want one that names the variable", name, err)
		}
	}
}
