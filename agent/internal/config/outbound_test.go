package config

import (
	"strings"
	"testing"
)

func TestLoadOutboundDefaultsToThePlainInternet(t *testing.T) {
	cfg, err := Load(env(nil))
	if err != nil {
		t.Fatal(err)
	}
	if o := cfg.Outbound; o.Proxy != "" || o.CAFile != "" || o.SystemDNS || o.DNSResolvers != nil || o.ACMEDirectory != "" {
		t.Errorf("Outbound = %+v, want nothing set", o)
	}
}

func TestLoadKeepsTheProxysHostAndNeverItsPassword(t *testing.T) {
	cfg, err := Load(env(map[string]string{"HTTPS_PROXY": "http://alice:hunter2secret@proxy.example.com:3128"}))
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Outbound.Proxy != "proxy.example.com:3128" {
		t.Errorf("Proxy = %q", cfg.Outbound.Proxy)
	}

	_, err = Load(env(map[string]string{"HTTP_PROXY": "ftp://alice:hunter2secret@proxy.example.com"}))
	if err == nil || !strings.HasPrefix(err.Error(), "HTTP_PROXY: ") || strings.Contains(err.Error(), "hunter2secret") {
		t.Errorf("err = %v, want the variable named and the value left out", err)
	}
}

func TestLoadDNSResolvers(t *testing.T) {
	cfg, err := Load(env(map[string]string{EnvDNSResolvers: " System "}))
	if err != nil || !cfg.Outbound.SystemDNS || cfg.Outbound.DNSResolvers != nil {
		t.Errorf("system: %+v, %v", cfg.Outbound, err)
	}
	cfg, err = Load(env(map[string]string{EnvDNSResolvers: "10.0.0.2, 10.0.0.3:5353,fd00::53"}))
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(cfg.Outbound.DNSResolvers, " "); got != "10.0.0.2:53 10.0.0.3:5353 [fd00::53]:53" || cfg.Outbound.SystemDNS {
		t.Errorf("DNSResolvers = %q", got)
	}
	for _, bad := range []string{"dns.example.com", "10.0.0.2,", "10.0.0.2:0", "10.0.0.2:dns-port"} {
		if _, err := Load(env(map[string]string{EnvDNSResolvers: bad})); err == nil || !strings.Contains(err.Error(), EnvDNSResolvers) {
			t.Errorf("%q: err = %v", bad, err)
		}
	}
}

func TestLoadACMEDirectory(t *testing.T) {
	with := func(dir string) map[string]string {
		return map[string]string{EnvACMEDirectory: dir, EnvCaddyAdmin: "unix//run/caddy/admin.sock"}
	}
	cfg, err := Load(env(with(" https://ca.example.internal/acme/acme/directory ")))
	if err != nil || cfg.Outbound.ACMEDirectory != "https://ca.example.internal/acme/acme/directory" {
		t.Errorf("ACMEDirectory = %q, %v", cfg.Outbound.ACMEDirectory, err)
	}
	for name, dir := range map[string]string{
		"plain http":    "http://ca.example.internal/directory",
		"no scheme":     "ca.example.internal/directory",
		"credentials":   "https://user:pass@ca.example.internal/directory",
		"a query":       "https://ca.example.internal/directory?x=1",
		"a placeholder": "https://ca.example.internal/{env.SECRET}",
	} {
		if _, err := Load(env(with(dir))); err == nil || !strings.HasPrefix(err.Error(), EnvACMEDirectory+": ") {
			t.Errorf("%s: err = %v", name, err)
		}
	}
	_, err = Load(env(map[string]string{EnvACMEDirectory: "https://ca.example.internal/directory"}))
	if err == nil || !strings.Contains(err.Error(), EnvCaddyAdmin) {
		t.Errorf("without a proxy to obtain certificates: %v", err)
	}
}

func TestLoadCAFileMustBeAnAbsolutePath(t *testing.T) {
	if _, err := Load(env(map[string]string{EnvCAFile: "ca.pem"})); err == nil || !strings.Contains(err.Error(), "not an absolute path") {
		t.Errorf("err = %v", err)
	}
}
