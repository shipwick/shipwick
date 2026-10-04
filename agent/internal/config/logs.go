package config

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/shipwick/shipwick/pkg/spec"
)

const (
	EnvLogRetentionDays = "SHIPWICK_LOG_RETENTION_DAYS"
	EnvLogRetentionSize = "SHIPWICK_LOG_RETENTION_SIZE"
)

// Defaults of the log archive: two weeks, and a size that is a twentieth of
// the smallest disk Shipwick is installed on.
const (
	defaultLogRetentionDays = 14
	defaultLogRetentionSize = 1 << 30
	maxLogRetentionDays     = 365
)

// LogArchive says how much of the output of ended containers the agent
// keeps.
type LogArchive struct {
	// Dir is <data dir>/logs.
	Dir string
	// RetentionDays is how long an entry is kept after its container ended.
	RetentionDays int
	// MaxBytes is what the archive may take on the disk; the oldest entries
	// go first. Zero: nothing is kept.
	MaxBytes int64
}

func (c *Config) loadLogArchive(getenv func(string) string) error {
	c.Logs = LogArchive{Dir: filepath.Join(c.DataDir, "logs"), RetentionDays: defaultLogRetentionDays, MaxBytes: defaultLogRetentionSize}
	if v := strings.TrimSpace(getenv(EnvLogRetentionDays)); v != "" {
		n, err := strconv.Atoi(v)
		if err != nil || n < 1 || n > maxLogRetentionDays {
			return fmt.Errorf("%s: invalid value %q (expected a number of days from 1 to %d)", EnvLogRetentionDays, v, maxLogRetentionDays)
		}
		c.Logs.RetentionDays = n
	}
	switch v := strings.TrimSpace(getenv(EnvLogRetentionSize)); v {
	case "":
	case "0":
		c.Logs.MaxBytes = 0
	default:
		n, err := spec.ParseMemory(v)
		if err != nil {
			return fmt.Errorf("%s: %w (expected a size such as 500mb or 2gb, or 0 to keep nothing)", EnvLogRetentionSize, err)
		}
		c.Logs.MaxBytes = n
	}
	return nil
}
