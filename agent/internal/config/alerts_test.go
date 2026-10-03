package config

import (
	"strings"
	"testing"
)

func TestLoadAlertThresholds(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.AlertMemoryPercent != 0 || cfg.AlertDiskPercent != 0 {
		t.Errorf("unset thresholds = %d, %d; zero leaves the defaults to the engine", cfg.AlertMemoryPercent, cfg.AlertDiskPercent)
	}
	cfg, err = Load(env(map[string]string{EnvAlertMemoryPercent: "80", EnvAlertDiskPercent: " 70% "}))
	if err != nil || cfg.AlertMemoryPercent != 80 || cfg.AlertDiskPercent != 70 {
		t.Errorf("thresholds = %d, %d, err = %v", cfg.AlertMemoryPercent, cfg.AlertDiskPercent, err)
	}
	if cfg, err := Load(env(map[string]string{EnvAlertMemoryPercent: "100", EnvAlertDiskPercent: "94"})); err != nil || cfg.AlertMemoryPercent != 100 || cfg.AlertDiskPercent != 94 {
		t.Errorf("the upper bounds are valid: %+v, %v", cfg, err)
	}

	bad := []struct{ name, value, want string }{
		{EnvAlertMemoryPercent, "ninety", "50 to 100"},
		{EnvAlertMemoryPercent, "101", "50 to 100"},
		{EnvAlertMemoryPercent, "49", "50 to 100"},
		{EnvAlertMemoryPercent, "0.9", "50 to 100"},
		// The disk alert turns critical at 95: a warning there would never be one.
		{EnvAlertDiskPercent, "95", "50 to 94"},
		{EnvAlertDiskPercent, "10", "50 to 94"},
	}
	for _, tt := range bad {
		_, err := Load(env(map[string]string{tt.name: tt.value}))
		if err == nil || !strings.Contains(err.Error(), tt.name) || !strings.Contains(err.Error(), tt.want) {
			t.Errorf("%s=%s: err = %v; want the variable named and the range %s", tt.name, tt.value, err, tt.want)
		}
	}
}
