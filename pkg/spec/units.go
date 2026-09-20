package spec

import (
	"fmt"
	"math"
	"regexp"
	"strconv"
	"strings"
)

// MinMemoryBytes is the smallest memory limit Docker accepts.
const MinMemoryBytes = 6 * 1024 * 1024

var memoryPattern = regexp.MustCompile(`^([0-9]+(?:\.[0-9]+)?)\s*([a-z]*)$`)

// Units are binary (1gb = 1024mb), matching Docker's own convention.
var memoryUnits = map[string]float64{
	"":    1,
	"b":   1,
	"k":   1 << 10,
	"kb":  1 << 10,
	"kib": 1 << 10,
	"m":   1 << 20,
	"mb":  1 << 20,
	"mib": 1 << 20,
	"g":   1 << 30,
	"gb":  1 << 30,
	"gib": 1 << 30,
}

// ParseMemory converts a human-readable size such as "512mb" or "1.5gb" to bytes.
func ParseMemory(s string) (int64, error) {
	m := memoryPattern.FindStringSubmatch(strings.ToLower(strings.TrimSpace(s)))
	if m == nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	unit, ok := memoryUnits[m[2]]
	if !ok {
		return 0, fmt.Errorf("invalid value %q: unknown unit %q", s, m[2])
	}
	n, err := strconv.ParseFloat(m[1], 64)
	if err != nil {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	bytes := n * unit
	if bytes > math.MaxInt64/2 {
		return 0, fmt.Errorf("invalid value %q: too large", s)
	}
	if int64(bytes) < MinMemoryBytes {
		return 0, fmt.Errorf("invalid value %q: minimum is 6mb", s)
	}
	return int64(bytes), nil
}

// FormatMemory renders bytes the way users write them in deploy.yaml.
func FormatMemory(bytes int64) string {
	switch {
	case bytes >= 1<<30:
		return trimFloat(float64(bytes)/(1<<30)) + " GB"
	case bytes >= 1<<20:
		return trimFloat(float64(bytes)/(1<<20)) + " MB"
	case bytes >= 1<<10:
		return trimFloat(float64(bytes)/(1<<10)) + " KB"
	}
	return strconv.FormatInt(bytes, 10) + " B"
}

func trimFloat(f float64) string {
	return strconv.FormatFloat(math.Round(f*10)/10, 'f', -1, 64)
}

// MaxCPU is a sanity bound; no single VPS comes close.
const MaxCPU = 512

// ParseCPU parses a CPU limit expressed in cores, e.g. "2" or "0.5".
func ParseCPU(s string) (float64, error) {
	n, err := strconv.ParseFloat(strings.TrimSpace(s), 64)
	if err != nil || math.IsNaN(n) || math.IsInf(n, 0) {
		return 0, fmt.Errorf("invalid value %q", s)
	}
	if n < 0.01 || n > MaxCPU {
		return 0, fmt.Errorf("invalid value %q: must be between 0.01 and %d", s, MaxCPU)
	}
	return n, nil
}
