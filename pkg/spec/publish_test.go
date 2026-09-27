package spec

import (
	"errors"
	"strings"
	"testing"
)

const publishConfig = `
name: postgres
image: postgres:17
deploy:
  strategy: recreate
publish:
  - port: 5432
    host: 15432
    address: 10.0.0.5
  - port: 5353
    protocol: UDP
`

func TestPublish(t *testing.T) {
	app, err := Parse([]byte(publishConfig))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	want := []Publish{
		{Port: 5432, Host: 15432, Address: "10.0.0.5", Protocol: "tcp"},
		{Port: 5353, Host: 5353, Protocol: "udp"},
	}
	if len(app.Publish) != len(want) || app.Publish[0] != want[0] || app.Publish[1] != want[1] {
		t.Errorf("publish = %+v, want %+v", app.Publish, want)
	}

	// The address is stored in its canonical form: that is what the agent
	// compares when it looks for two applications on one port.
	app, err = Parse([]byte("name: db\nimage: redis\ndeploy:\n  strategy: recreate\npublish:\n  - port: 6379\n    address: '::ffff:10.0.0.5'\n"))
	if err != nil || app.Publish[0].Address != "10.0.0.5" {
		t.Errorf("address = %q, %v; want the canonical form", app.Publish[0].Address, err)
	}
}

func TestPublishValidationErrors(t *testing.T) {
	const head = "name: db\nimage: postgres\ndeploy:\n  strategy: recreate\n"
	for _, tt := range []struct{ name, config, field, msg string }{
		{"needs recreate", "name: db\nimage: postgres\npublish:\n  - port: 5432\n", "deploy.strategy", `must be "recreate"`},
		{"needs one replica", "name: db\nimage: postgres\nreplicas: 2\ndeploy:\n  strategy: recreate\npublish:\n  - port: 5432\n", "replicas", "must be 1"},
		{"port required", head + "publish:\n  - host: 5432\n", "publish[0].port", "is required"},
		{"port out of range", head + "publish:\n  - port: 70000\n", "publish[0].port", "invalid value 70000"},
		{"host out of range", head + "publish:\n  - port: 5432\n    host: 0\n", "publish[0].host", "invalid value 0"},
		{"http is the proxy's", head + "publish:\n  - port: 8080\n    host: 80\n", "publish[0].host", "the proxy listens there"},
		{"https is the proxy's", head + "publish:\n  - port: 443\n", "publish[0].host", "the proxy listens there"},
		{"bad protocol", head + "publish:\n  - port: 5432\n    protocol: sctp\n", "publish[0].protocol", `invalid value "sctp"`},
		{"bad address", head + "publish:\n  - port: 5432\n    address: db.internal\n", "publish[0].address", `invalid value "db.internal"`},
		{"duplicate host port", head + "publish:\n  - port: 5432\n  - port: 5433\n    host: 5432\n", "publish[1].host", "published twice"},
		{"duplicate on one address", head + "publish:\n  - port: 5432\n    address: 10.0.0.5\n  - port: 5433\n    host: 5432\n    address: 10.0.0.5\n", "publish[1].host", "published twice"},
		{"too many", head + "publish:\n" + strings.Repeat("  - port: 1000\n", MaxPublish+1), "publish", "too many"},
	} {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.config))
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("err = %v", err)
			}
			for _, f := range verr.Fields {
				if f.Field == tt.field && strings.Contains(f.Message, tt.msg) {
					return
				}
			}
			t.Errorf("no error for %q containing %q; got %+v", tt.field, tt.msg, verr.Fields)
		})
	}

	// The same port on two addresses, or for two protocols, is two bindings.
	for _, config := range []string{
		head + "publish:\n  - port: 5432\n    address: 10.0.0.5\n  - port: 5432\n    address: 10.0.0.6\n",
		head + "publish:\n  - port: 5432\n  - port: 5432\n    protocol: udp\n",
	} {
		if _, err := Parse([]byte(config)); err != nil {
			t.Errorf("Parse(%q): %v", config, err)
		}
	}

	// With volumes, the strategy and replica rules are reported once, not
	// once per feature.
	_, err := Parse([]byte("name: db\nimage: postgres\nreplicas: 2\nvolumes:\n  - name: data\n    path: /data\npublish:\n  - port: 5432\n"))
	var verr *ValidationError
	if !errors.As(err, &verr) {
		t.Fatalf("err = %v", err)
	}
	count := map[string]int{}
	for _, f := range verr.Fields {
		count[f.Field]++
	}
	if count["deploy.strategy"] != 1 || count["replicas"] != 1 {
		t.Errorf("strategy and replicas must be reported once each, got %+v", verr.Fields)
	}
}
