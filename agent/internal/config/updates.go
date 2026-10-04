package config

import (
	"fmt"
	"path/filepath"
	"strings"
)

// EnvUpdateCheck turns off the agent's daily question about a newer
// release: "off". Unset or "on": it asks.
const EnvUpdateCheck = "SHIPWICK_UPDATE_CHECK"

func (c *Config) loadUpdateCheck(getenv func(string) string) error {
	switch raw := strings.ToLower(strings.TrimSpace(getenv(EnvUpdateCheck))); raw {
	case "", "on", "true", "1":
		c.UpdateCheck = true
	case "off", "false", "0":
		c.UpdateCheck = false
	default:
		return fmt.Errorf("%s: invalid value %q (expected on or off)", EnvUpdateCheck, raw)
	}
	return nil
}

// UpdateCheckPath is where the agent keeps what it last learned about
// releases, so that a restart does not ask again.
func (c Config) UpdateCheckPath() string {
	return filepath.Join(c.DataDir, "update-check.json")
}
