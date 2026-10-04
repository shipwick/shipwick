package config

import (
	"strings"
	"testing"
)

func TestTheUpdateCheckIsOnUnlessTurnedOff(t *testing.T) {
	for value, want := range map[string]bool{"": true, "on": true, "ON": true, "true": true, "1": true, "off": false, " Off ": false, "false": false, "0": false} {
		cfg, err := Load(env(map[string]string{EnvUpdateCheck: value}))
		if err != nil {
			t.Fatalf("%s=%q: %v", EnvUpdateCheck, value, err)
		}
		if cfg.UpdateCheck != want {
			t.Errorf("%s=%q: UpdateCheck = %v, want %v", EnvUpdateCheck, value, cfg.UpdateCheck, want)
		}
	}
}

func TestTheUpdateCheckRefusesAValueItDoesNotKnow(t *testing.T) {
	_, err := Load(env(map[string]string{EnvUpdateCheck: "weekly"}))
	if err == nil || !strings.Contains(err.Error(), `SHIPWICK_UPDATE_CHECK: invalid value "weekly" (expected on or off)`) {
		t.Fatalf("err = %v", err)
	}
}
