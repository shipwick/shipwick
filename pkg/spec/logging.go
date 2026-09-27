package spec

import (
	"fmt"
	"net"
	"net/url"
	"regexp"
	"slices"
	"sort"
	"strconv"
	"strings"
)

// LogDrivers are the Docker logging drivers an application may select. The
// list is closed because a driver's options reach the daemon verbatim, and
// every driver here is one whose options are checked below.
var LogDrivers = []string{"json-file", "local", "syslog", "journald", "gelf", "fluentd", "awslogs", "splunk"}

// Bounds for logging.options.
const (
	MaxLogOptions     = 20
	MaxLogOptionBytes = 1024
)

var logOptionKeyPattern = regexp.MustCompile(`^[a-z][a-z0-9-]*$`)

// logAddressOptions name the collector. Only a network address is accepted:
// a socket is a path on the server, and Shipwick never touches host paths.
var logAddressOptions = map[string]struct {
	schemes []string
	example string
}{
	"syslog-address":  {[]string{"udp", "tcp", "tcp+tls"}, "udp://logs.example.com:514"},
	"gelf-address":    {[]string{"udp", "tcp"}, "udp://logs.example.com:12201"},
	"fluentd-address": {[]string{"tcp", "tls"}, "tcp://logs.example.com:24224"},
}

// logFileOptions make the daemon read a file on the server, which no
// application gets to name.
var logFileOptions = []string{"syslog-tls-ca-cert", "syslog-tls-cert", "syslog-tls-key", "splunk-cafile", "splunk-capath"}

// validateLogging checks the logging driver and its options.
func (r raw) validateLogging(verr *ValidationError) *Logging {
	if r.Logging == nil {
		return nil
	}
	l := &Logging{Driver: strings.TrimSpace(r.Logging.Driver)}
	switch {
	case l.Driver == "":
		verr.add("logging.driver", "is required when logging is set", strings.Join(LogDrivers, ", "))
	case !slices.Contains(LogDrivers, l.Driver):
		verr.add("logging.driver", fmt.Sprintf("invalid value %q", r.Logging.Driver), strings.Join(LogDrivers, ", "))
	}

	if len(r.Logging.Options) == 0 {
		return l
	}
	if n := len(r.Logging.Options); n > MaxLogOptions {
		verr.add("logging.options", fmt.Sprintf("too many (%d)", n), fmt.Sprintf("at most %d", MaxLogOptions))
		return l
	}
	keys := make([]string, 0, len(r.Logging.Options))
	for k := range r.Logging.Options {
		keys = append(keys, k)
	}
	sort.Strings(keys) // deterministic error order
	l.Options = make(map[string]string, len(keys))
	for _, k := range keys {
		v := r.Logging.Options[k]
		field := "logging.options." + k
		switch {
		case !logOptionKeyPattern.MatchString(k):
			verr.add(field, "invalid option name", "lowercase letters, digits and dashes, e.g. gelf-address")
			continue
		case slices.Contains(logFileOptions, k):
			verr.add(field, "names a file on the server, which Shipwick never reads", "leave it out and give the collector a certificate the server trusts")
			continue
		case len(v) > MaxLogOptionBytes:
			verr.add(field, fmt.Sprintf("value is too long (%d characters)", len(v)), fmt.Sprintf("at most %d", MaxLogOptionBytes))
			continue
		case strings.ContainsAny(v, "\x00\r\n"):
			verr.add(field, "value must not contain newlines or NUL bytes", "")
			continue
		}
		if addr, ok := logAddressOptions[k]; ok {
			if err := validateLogAddress(v, addr.schemes); err != nil {
				verr.add(field, err.Error(), addr.example)
				continue
			}
		}
		l.Options[k] = v
	}
	return l
}

// validateLogAddress accepts scheme://host:port and nothing else.
func validateLogAddress(s string, schemes []string) error {
	if !strings.Contains(s, "://") {
		return fmt.Errorf("invalid value %q: must be scheme://host:port", s)
	}
	u, err := url.Parse(s)
	if err != nil {
		return fmt.Errorf("invalid value %q: must be scheme://host:port", s)
	}
	switch u.Scheme {
	case "unix", "unixgram":
		return fmt.Errorf("invalid value %q: a socket is a path on the server, which Shipwick never mounts; use a network address", s)
	}
	if !slices.Contains(schemes, u.Scheme) {
		return fmt.Errorf("invalid value %q: unknown scheme %q, use %s", s, u.Scheme, strings.Join(schemes, ", "))
	}
	if u.Opaque != "" || u.User != nil || u.Path != "" || u.RawQuery != "" || u.Fragment != "" {
		return fmt.Errorf("invalid value %q: must be scheme://host:port, nothing after the port", s)
	}
	host, port, err := net.SplitHostPort(u.Host)
	if err != nil || host == "" {
		return fmt.Errorf("invalid value %q: must be scheme://host:port", s)
	}
	if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
		return fmt.Errorf("invalid value %q: port must be a number between 1 and 65535", s)
	}
	return nil
}
