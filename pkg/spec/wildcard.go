package spec

import (
	"fmt"
	"strings"
)

// IsWildcard reports whether hostname stands for every name one label below
// a domain: *.example.com.
func IsWildcard(hostname string) bool {
	return strings.HasPrefix(hostname, "*.")
}

// ValidateHostname is ValidateDomain for the hostnames an application is
// served at, which may also be a wildcard: one leading label that is exactly
// "*", and a plain hostname after it. The star matches one label, as it does
// in a certificate: *.example.com is a.example.com, and neither example.com
// nor a.b.example.com.
func ValidateHostname(hostname string) error {
	if !strings.Contains(hostname, "*") || len(hostname) > 253 {
		return ValidateDomain(hostname)
	}
	rest, ok := strings.CutPrefix(hostname, "*.")
	if !ok || ValidateDomain(rest) != nil {
		return fmt.Errorf("invalid value %q: a wildcard is one leading label and nothing else", hostname)
	}
	return nil
}
