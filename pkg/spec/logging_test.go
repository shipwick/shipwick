package spec

import (
	"errors"
	"strings"
	"testing"
)

func TestLogging(t *testing.T) {
	app, err := Parse([]byte("name: api\nimage: app:1\nlogging:\n  driver: gelf\n  options:\n    gelf-address: udp://logs.example.com:12201\n    tag: \"{{.Name}}\"\n"))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if app.Logging == nil || app.Logging.Driver != "gelf" || len(app.Logging.Options) != 2 || app.Logging.Options["tag"] != "{{.Name}}" {
		t.Errorf("logging = %+v", app.Logging)
	}

	if app, err := Parse([]byte("name: api\nimage: app:1\nlogging:\n  driver: local\n")); err != nil || app.Logging.Driver != "local" || app.Logging.Options != nil {
		t.Errorf("driver alone: %+v, %v", app.Logging, err)
	}
	if app, err := Parse([]byte("name: api\nimage: app:1\nlogging:\n  driver: json-file\n  options:\n    max-file: 5\n")); err != nil || app.Logging.Options["max-file"] != "5" {
		t.Errorf("a number is a fine option value: %+v, %v", app.Logging, err)
	}
	if app, err := Parse([]byte("name: api\nimage: app:1\n")); err != nil || app.Logging != nil {
		t.Errorf("no logging block: %+v, %v", app.Logging, err)
	}

	for _, addr := range []string{
		"syslog-address: tcp+tls://logs.example.com:6514",
		"syslog-address: udp://10.0.0.7:514",
		"gelf-address: tcp://[2001:db8::1]:12201",
		"fluentd-address: tls://logs.example.com:24224",
	} {
		key, _, _ := strings.Cut(addr, ":")
		driver := strings.TrimSuffix(key, "-address")
		if _, err := Parse([]byte("name: api\nimage: app:1\nlogging:\n  driver: " + driver + "\n  options:\n    " + addr + "\n")); err != nil {
			t.Errorf("%s: %v", addr, err)
		}
	}

	options := func(n int) string {
		var b strings.Builder
		for i := 0; i < n; i++ {
			b.WriteString("    opt-")
			b.WriteString(strings.Repeat("x", i+1))
			b.WriteString(": v\n")
		}
		return b.String()
	}
	for _, tt := range []struct{ name, config, field, msg string }{
		{"driver is required", "logging:\n  options:\n    tag: x\n", "logging.driver", "is required"},
		{"unknown driver", "logging:\n  driver: GELF\n", "logging.driver", `invalid value "GELF"`},
		{"option name", "logging:\n  driver: gelf\n  options:\n    Gelf-Address: udp://h:1\n", "logging.options.Gelf-Address", "invalid option name"},
		{"unix socket", "logging:\n  driver: syslog\n  options:\n    syslog-address: unix:///dev/log\n", "logging.options.syslog-address", "path on the server"},
		{"unixgram socket", "logging:\n  driver: syslog\n  options:\n    syslog-address: unixgram:///dev/log\n", "logging.options.syslog-address", "path on the server"},
		{"address without scheme", "logging:\n  driver: gelf\n  options:\n    gelf-address: logs.example.com:12201\n", "logging.options.gelf-address", "scheme://host:port"},
		{"scheme the driver does not speak", "logging:\n  driver: gelf\n  options:\n    gelf-address: tcp+tls://logs.example.com:12201\n", "logging.options.gelf-address", "unknown scheme"},
		{"address without port", "logging:\n  driver: gelf\n  options:\n    gelf-address: udp://logs.example.com\n", "logging.options.gelf-address", "scheme://host:port"},
		{"address with a path", "logging:\n  driver: fluentd\n  options:\n    fluentd-address: tcp://logs.example.com:24224/x\n", "logging.options.fluentd-address", "nothing after the port"},
		{"address with credentials", "logging:\n  driver: gelf\n  options:\n    gelf-address: udp://u:p@logs.example.com:12201\n", "logging.options.gelf-address", "nothing after the port"},
		{"port out of range", "logging:\n  driver: gelf\n  options:\n    gelf-address: udp://logs.example.com:99999\n", "logging.options.gelf-address", "port must be"},
		{"file on the server", "logging:\n  driver: syslog\n  options:\n    syslog-tls-ca-cert: /etc/ssl/ca.pem\n", "logging.options.syslog-tls-ca-cert", "names a file on the server"},
		{"newline in a value", "logging:\n  driver: gelf\n  options:\n    tag: \"a\\nb\"\n", "logging.options.tag", "newlines"},
		{"value too long", "logging:\n  driver: gelf\n  options:\n    tag: " + strings.Repeat("x", 1025) + "\n", "logging.options.tag", "too long"},
		{"too many options", "logging:\n  driver: gelf\n  options:\n" + options(21), "logging.options", "too many (21)"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte("name: api\nimage: app:1\n" + tt.config))
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v", err)
			}
			for _, f := range verr.Fields {
				if f.Field == tt.field && strings.Contains(f.Message, tt.msg) {
					if tt.field == "logging.driver" && !strings.Contains(f.Expected, "json-file, local, syslog") {
						t.Errorf("the drivers must be listed, got %q", f.Expected)
					}
					return
				}
			}
			t.Errorf("no error for %q containing %q; got %+v", tt.field, tt.msg, verr.Fields)
		})
	}
}
