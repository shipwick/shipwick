package commands

import (
	"bytes"
	"context"
	"crypto/tls"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// fakePrograms stands in for the programs shipwick runs on this machine. It
// records every argv and answers each remote command through respond.
type fakePrograms struct {
	argvs   [][]string
	respond func(argv []string, stdout, stderr io.Writer) error
}

func (p *fakePrograms) exec(_ context.Context, argv []string, stdout, stderr io.Writer) error {
	p.argvs = append(p.argvs, argv)
	if p.respond == nil {
		return nil
	}
	return p.respond(argv, stdout, stderr)
}

// remote is the command an ssh argv runs on the server.
func remote(argv []string) string { return argv[len(argv)-1] }

const installedToken = "0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"

// installerSummary is print_summary of scripts/install.sh, as it arrives over
// ssh: no colors, the token on a line of its own.
const installerSummary = `✓ Docker 29.8.0 with Compose 5.5.1
✓ Wrote /opt/shipwick/.env
✓ Started the Shipwick services
✓ The agent is healthy

Shipwick is running.

  API token (also in /opt/shipwick/.env — it is root on this server, treat it so):

      ` + installedToken + `

  From your laptop or CI:   shipwick login --url https://agent.example.com
  Dashboard:                https://dashboard.example.com
`

// freshServer answers like a Linux box without Docker: the version check
// fails, get.docker.com succeeds, the installer prints a new token.
func freshServer(argv []string, stdout, stderr io.Writer) error {
	switch remote(argv) {
	case remoteUname:
		fmt.Fprintln(stdout, "Linux x86_64")
	case remoteHasDocker:
		return errors.New("exit status 1")
	case remoteInstallDocker:
		fmt.Fprintln(stdout, "# Executing docker install script")
	default:
		fmt.Fprint(stdout, installerSummary)
	}
	return nil
}

func TestServerInstallRunsSSHWithoutAShell(t *testing.T) {
	f := newFakeAgent(t)
	programs := &fakePrograms{respond: freshServer}
	f.local = localOptions{exec: programs.exec, lookupHost: func(_ context.Context, host string) ([]string, error) {
		if host == "203.0.113.10" {
			t.Fatal("an IP address needs no lookup")
		}
		return nil, errors.New("no such host") // the records do not exist yet
	}}
	config := filepath.Join(t.TempDir(), "config.yaml")
	f.env = map[string]string{cliconfig.EnvConfig: config}

	out, _, err := f.run(t.TempDir(), "server", "install", "root@203.0.113.10",
		"--agent-domain", "Agent.Example.com", "--dashboard-domain", "dashboard.example.com", "--context", "prod")
	if err != nil {
		t.Fatalf("server install: %v\n%s", err, out)
	}

	ssh := func(cmd string) []string {
		return []string{"ssh", "-o", "BatchMode=yes", "-o", "StrictHostKeyChecking=accept-new", "-o", "ConnectTimeout=15", "root@203.0.113.10", "--", cmd}
	}
	want := [][]string{
		ssh("uname -sm"),
		ssh("command -v docker >/dev/null 2>&1"),
		ssh("curl -fsSL https://get.docker.com | sh"),
		ssh("curl -fsSL https://get.shipwick.com | SHIPWICK_AGENT_DOMAIN='agent.example.com' SHIPWICK_DASHBOARD_DOMAIN='dashboard.example.com' sh"),
	}
	if !reflect.DeepEqual(programs.argvs, want) {
		t.Errorf("ssh was run as\n%q\nwant\n%q", programs.argvs, want)
	}

	assertInOrder(t, out, []string{
		"✓ Connected to root@203.0.113.10 (x86_64)",
		"Docker is not installed. Installing it",
		"  # Executing docker install script",
		"✓ Installed Docker",
		"Running the Shipwick installer",
		"  ✓ The agent is healthy",
		"(the API token — saved to your shipwick config, not shown)",
		"✓ Shipwick is running on root@203.0.113.10",
		"✓ Saved the API token as context prod (https://agent.example.com), now current",
		"Create these DNS records, DNS only (not proxied):",
		"A     agent.example.com  →  203.0.113.10",
		"A     dashboard.example.com  →  203.0.113.10",
		"Next: in your project, run: shipwick init",
		"shipwick doctor",
	})
	if strings.Contains(out, installedToken) {
		t.Errorf("the token must not be echoed:\n%s", out)
	}

	saved, err := cliconfig.Load(config)
	if err != nil {
		t.Fatal(err)
	}
	if saved.Current != "prod" || saved.Contexts["prod"] != (cliconfig.Context{URL: "https://agent.example.com", Token: installedToken}) {
		t.Errorf("unexpected saved config: %+v", saved)
	}
}

func TestServerInstallWithDockerAndNoHostname(t *testing.T) {
	f := newFakeAgent(t)
	programs := &fakePrograms{respond: func(argv []string, stdout, stderr io.Writer) error {
		switch remote(argv) {
		case remoteUname:
			fmt.Fprintln(stdout, "Linux aarch64")
		case remoteHasDocker:
		case remoteDockerVersion:
			fmt.Fprintln(stdout, "29.8.0")
		default:
			fmt.Fprint(stdout, strings.ReplaceAll(installerSummary, "https://agent.example.com", ""))
		}
		return nil
	}}
	f.local = localOptions{exec: programs.exec, lookupHost: func(_ context.Context, host string) ([]string, error) {
		if host != "box.example.net" {
			t.Errorf("resolved %q", host)
		}
		return []string{"203.0.113.10", "2001:db8::10"}, nil
	}}
	config := filepath.Join(t.TempDir(), "config.yaml")
	f.env = map[string]string{cliconfig.EnvConfig: config}

	out, _, err := f.run(t.TempDir(), "server", "install", "deploy@box.example.net", "--version", "v0.4.0")
	if err != nil {
		t.Fatalf("server install: %v\n%s", err, out)
	}
	if n := len(programs.argvs); n != 4 || !strings.Contains(remote(programs.argvs[2]), "docker version") {
		t.Errorf("Docker being present, nothing should be installed but Shipwick: %q", programs.argvs)
	}
	if got := remote(programs.argvs[3]); got != "curl -fsSL https://get.shipwick.com | SHIPWICK_AGENT_DOMAIN='' SHIPWICK_DASHBOARD_DOMAIN='' SHIPWICK_VERSION='v0.4.0' sh" {
		t.Errorf("installer command: %s", got)
	}
	assertInOrder(t, out, []string{
		"✓ Connected to deploy@box.example.net (aarch64)",
		"✓ Docker 29.8.0",
		"✓ Saved the API token as context box.example.net (http://127.0.0.1:9000)",
		"reachable only through an SSH tunnel",
		"Next:",
	})
	if strings.Contains(out, "DNS records") {
		t.Errorf("no hostname, no DNS records:\n%s", out)
	}
}

func TestServerInstallPrintsRecordsForAResolvedHost(t *testing.T) {
	f := newFakeAgent(t)
	programs := &fakePrograms{respond: freshServer}
	f.local = localOptions{exec: programs.exec, lookupHost: func(_ context.Context, host string) ([]string, error) {
		if host == "box.example.net" {
			return []string{"203.0.113.10", "2001:db8::10"}, nil
		}
		return nil, errors.New("no such host")
	}}
	out, _, err := f.run(t.TempDir(), "server", "install", "root@box.example.net", "--agent-domain", "agent.example.com")
	if err != nil {
		t.Fatalf("server install: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"Create these DNS records",
		"A     agent.example.com  →  203.0.113.10",
		"AAAA  agent.example.com  →  2001:db8::10",
	})

	// On an upgrade the record exists: it is confirmed, not asked for.
	f.local.lookupHost = func(context.Context, string) ([]string, error) {
		return []string{"203.0.113.10", "2001:db8::10"}, nil
	}
	out, _, err = f.run(t.TempDir(), "server", "install", "root@box.example.net", "--agent-domain", "agent.example.com")
	if err != nil {
		t.Fatalf("server install: %v\n%s", err, out)
	}
	if !strings.Contains(out, "✓ agent.example.com already points at 203.0.113.10, 2001:db8::10") || strings.Contains(out, "Create these DNS records") {
		t.Errorf("an existing record is confirmed, not asked for:\n%s", out)
	}
}

func TestServerInstallUpgradeKeepsOrAsksForTheToken(t *testing.T) {
	// The installer prints no token on an upgrade.
	upgrade := func(argv []string, stdout, stderr io.Writer) error {
		if err := freshServer(argv, io.Discard, stderr); err != nil {
			return err
		}
		if strings.Contains(remote(argv), "get.shipwick.com") {
			fmt.Fprintln(stdout, "  Your API token is unchanged: SHIPWICK_AGENT_TOKEN in /opt/shipwick/.env")
		} else if remote(argv) == remoteUname {
			fmt.Fprintln(stdout, "Linux x86_64")
		}
		return nil
	}

	f := newFakeAgent(t)
	f.local = localOptions{exec: (&fakePrograms{respond: upgrade}).exec}
	config := filepath.Join(t.TempDir(), "config.yaml")
	f.env = map[string]string{cliconfig.EnvConfig: config}
	if err := cliconfig.Save(config, cliconfig.Config{Current: "other", Contexts: map[string]cliconfig.Context{
		"other":        {URL: "https://other.example.com", Token: "x"},
		"203.0.113.10": {URL: "https://agent.example.com", Token: "kept"},
	}}); err != nil {
		t.Fatal(err)
	}

	out, _, err := f.run(t.TempDir(), "server", "install", "root@203.0.113.10", "--agent-domain", "agent.example.com")
	if err != nil {
		t.Fatalf("server install: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"✓ Context 203.0.113.10 (https://agent.example.com) is current; its token is unchanged"})
	saved, _ := cliconfig.Load(config)
	if saved.Current != "203.0.113.10" || saved.Contexts["203.0.113.10"].Token != "kept" {
		t.Errorf("the saved token must survive an upgrade: %+v", saved)
	}

	// A first install whose token was not seen: say so, never fetch it.
	f.local = localOptions{exec: (&fakePrograms{respond: upgrade}).exec}
	out, _, err = f.run(t.TempDir(), "server", "install", "root@203.0.113.11", "--agent-domain", "agent2.example.com")
	if err != nil {
		t.Fatalf("server install: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"! Context 203.0.113.11 (https://agent2.example.com) was saved without a token",
		"/opt/shipwick/.env",
		"shipwick login --context 203.0.113.11",
	})
	saved, _ = cliconfig.Load(config)
	if saved.Contexts["203.0.113.11"].Token != "" {
		t.Errorf("no token was printed, none may be saved: %+v", saved)
	}
}

func TestServerInstallRefusesWhatCouldReachAShell(t *testing.T) {
	f := newFakeAgent(t)
	programs := &fakePrograms{respond: freshServer}
	f.local = localOptions{exec: programs.exec}
	cases := map[string][]string{
		"no user":            {"203.0.113.10"},
		"uppercase user":     {"Root@203.0.113.10"},
		"user with a space":  {"ro ot@203.0.113.10"},
		"host with a quote":  {"root@host'example"},
		"host with a semi":   {"root@example.com;id"},
		"host as an option":  {"root@-oProxyCommand=id"},
		"scheme in a domain": {"root@203.0.113.10", "--agent-domain", "https://agent.example.com"},
		"space in a domain":  {"root@203.0.113.10", "--dashboard-domain", "a b.example.com"},
		"same hostnames":     {"root@203.0.113.10", "--agent-domain", "x.example.com", "--dashboard-domain", "x.example.com"},
		"odd version":        {"root@203.0.113.10", "--version", "latest; id"},
	}
	for name, args := range cases {
		_, _, err := f.run(t.TempDir(), append([]string{"server", "install"}, args...)...)
		if err == nil {
			t.Errorf("%s: accepted %q", name, args)
		}
	}
	if len(programs.argvs) != 0 {
		t.Errorf("nothing may run before the arguments are checked, ran %q", programs.argvs)
	}
}

func TestServerInstallExplainsSSHFailures(t *testing.T) {
	f := newFakeAgent(t)
	f.local = localOptions{exec: func(context.Context, []string, io.Writer, io.Writer) error {
		return &exec.Error{Name: "ssh", Err: exec.ErrNotFound}
	}}
	_, _, err := f.run(t.TempDir(), "server", "install", "root@203.0.113.10")
	if err == nil || !strings.Contains(err.Error(), "ssh was not found") || !strings.Contains(err.Error(), "OpenSSH") {
		t.Errorf("missing ssh: %v", err)
	}

	f.local = localOptions{exec: func(_ context.Context, _ []string, _, stderr io.Writer) error {
		fmt.Fprintln(stderr, "root@203.0.113.10: Permission denied (publickey).")
		return errors.New("exit status 255")
	}}
	_, _, err = f.run(t.TempDir(), "server", "install", "root@203.0.113.10")
	if err == nil || !strings.Contains(err.Error(), "Permission denied (publickey)") || !strings.Contains(err.Error(), "ssh-copy-id root@203.0.113.10") {
		t.Errorf("refused login: %v", err)
	}

	// A reinstalled server has a new key; ssh refuses it with a banner. The
	// advice is to forget the old key, not to copy a login key.
	f.local = localOptions{exec: func(_ context.Context, _ []string, _, stderr io.Writer) error {
		fmt.Fprintln(stderr, "@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\nIT IS POSSIBLE THAT SOMEONE IS DOING SOMETHING NASTY!\nHost key verification failed.")
		return errors.New("exit status 255")
	}}
	_, _, err = f.run(t.TempDir(), "server", "install", "root@203.0.113.10")
	if err == nil || !strings.Contains(err.Error(), "ssh-keygen -R 203.0.113.10") || strings.Contains(err.Error(), "ssh-copy-id") || strings.Contains(err.Error(), "@@@") {
		t.Errorf("changed host key: %v", err)
	}

	f.local = localOptions{exec: func(_ context.Context, argv []string, stdout, _ io.Writer) error {
		fmt.Fprintln(stdout, "Darwin arm64")
		return nil
	}}
	_, _, err = f.run(t.TempDir(), "server", "install", "me@laptop.local")
	if err == nil || !strings.Contains(err.Error(), "runs on Linux") {
		t.Errorf("a non-Linux target: %v", err)
	}
}

func TestInstallerTokenIsTheLineAfterTheHeading(t *testing.T) {
	if got := installerToken(strings.Split(installerSummary, "\n")); got != installedToken {
		t.Errorf("installerToken = %q", got)
	}
	before := strings.Split(installedToken+"\n"+installerSummary, "\n")
	if got := installerToken(before[:9]); got != "" {
		t.Errorf("a hex line before the heading is not the token, got %q", got)
	}
	if got := installerToken([]string{"  Your API token is unchanged: SHIPWICK_AGENT_TOKEN in /opt/shipwick/.env"}); got != "" {
		t.Errorf("an upgrade prints no token, got %q", got)
	}
}

func TestOpenUsesThePlatformOpener(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Domain: "api.example.com"}}
	for goos, want := range map[string][]string{
		"linux":   {"xdg-open", "https://api.example.com"},
		"darwin":  {"open", "https://api.example.com"},
		"windows": {"rundll32", "url.dll,FileProtocolHandler", "https://api.example.com"},
	} {
		programs := &fakePrograms{}
		f.local = localOptions{exec: programs.exec, goos: goos}
		out, _, err := f.run(writeConfig(t, validConfig), "open")
		if err != nil {
			t.Fatalf("%s: open: %v", goos, err)
		}
		if len(programs.argvs) != 1 || !reflect.DeepEqual(programs.argvs[0], want) {
			t.Errorf("%s: ran %q, want %q", goos, programs.argvs, want)
		}
		if !strings.Contains(out, "Opening https://api.example.com") {
			t.Errorf("%s: unexpected output:\n%s", goos, out)
		}
	}

	f.local = localOptions{exec: func(context.Context, []string, io.Writer, io.Writer) error {
		return &exec.Error{Name: "xdg-open", Err: exec.ErrNotFound}
	}, goos: "linux"}
	_, _, err := f.run(t.TempDir(), "open", "my-api")
	if err == nil || !strings.Contains(err.Error(), "xdg-open was not found") || !strings.Contains(err.Error(), "https://api.example.com") {
		t.Errorf("without an opener the URL must still be given: %v", err)
	}
}

func TestOpenWithoutADomainSaysWhereTheAppIs(t *testing.T) {
	f := newFakeAgent(t)
	programs := &fakePrograms{}
	f.local = localOptions{exec: programs.exec}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "web"}, Spec: &spec.App{Name: "web", Port: 3000}}
	_, _, err := f.run(t.TempDir(), "open", "web")
	if err == nil || !strings.Contains(err.Error(), "web has no domain; it is reachable from other applications at http://web:3000") {
		t.Errorf("unexpected error: %v", err)
	}
	if len(programs.argvs) != 0 {
		t.Errorf("nothing to open, ran %q", programs.argvs)
	}
}

// roundTrip lets a test answer https://<domain>/ without a network.
type roundTrip func(*http.Request) (*http.Response, error)

func (fn roundTrip) RoundTrip(r *http.Request) (*http.Response, error) { return fn(r) }

func answer(status int) *http.Response {
	return &http.Response{StatusCode: status, Body: io.NopCloser(strings.NewReader("")), Header: http.Header{}}
}

func notFound(host string) error {
	return &net.DNSError{Err: "no such host", Name: host, IsNotFound: true}
}

// doctorAgent is a fake agent with a release to compare against and one
// application, my-api at api.example.com, whose DNS the test decides.
func doctorAgent(t *testing.T, lookup func(host string) ([]string, error), front roundTrip) *fakeAgent {
	g := newFakeGitHub(t)
	g.tag = "v0.4.0"
	f := upgradeAgent(t, g, installed(t), "v0.3.1")
	f.server = api.Server{AgentVersion: "1.2.3", DockerVersion: "29.8.0", Proxy: api.ProxyStatus{Enabled: true, Reachable: true, Routes: 2},
		Token: api.TokenIdentity{Name: "laptop", Role: api.RoleAdmin}}
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Domain: "api.example.com", Redirects: []string{"www.example.com"}}}
	f.local = localOptions{
		lookupHost: func(_ context.Context, host string) ([]string, error) { return lookup(host) },
		dial: func(context.Context, string) error {
			t.Error("through 127.0.0.1 the server's ports are unknown")
			return nil
		},
		http: &http.Client{Transport: front},
	}
	return f
}

func TestDoctorReportsAHealthySetup(t *testing.T) {
	f := doctorAgent(t,
		func(string) ([]string, error) { return []string{"203.0.113.10"}, nil },
		func(r *http.Request) (*http.Response, error) {
			if r.URL.String() != "https://api.example.com/" {
				t.Errorf("fetched %s", r.URL)
			}
			return answer(200), nil
		})
	out, _, err := f.run(t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("doctor: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"! shipwick v0.3.1; v0.4.0 is available. Upgrade with: shipwick upgrade",
		"✓ Agent http://127.0.0.1:",
		"runs 1.2.3, the latest release",
		"✓ Token laptop (admin)",
		"✓ Docker 29.8.0 on the server",
		"✓ Proxy serving 2 routes",
		"! The agent is reached through 127.0.0.1",
		"✓ api.example.com → 203.0.113.10",
		"✓ www.example.com → 203.0.113.10",
		"✓ https://api.example.com/ answers HTTP 200",
		"No problems; 2 things worth a look.",
	})
}

func TestDoctorNamesWhatRunsWithoutALimitAndAServerWithoutSwap(t *testing.T) {
	f := doctorAgent(t,
		func(string) ([]string, error) { return []string{"203.0.113.10"}, nil },
		func(*http.Request) (*http.Response, error) { return answer(200), nil })
	none := int64(0)
	f.server.MemoryBytes, f.server.SwapBytes = 4<<30, &none
	f.server.UnlimitedMemory = []string{"postgres", "redis", "scrum-poker"}
	out, _, err := f.run(t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("worth a look, not a failure: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"✓ Proxy serving 2 routes",
		"! 3 applications run without a memory limit: postgres, redis, scrum-poker. One that leaks",
		"set resources.memory in deploy.yaml",
		"! The server has no swap: once its 4 GB of memory is used, the kernel kills a process at once. Add a swap file on the server",
		"No problems; 4 things worth a look.",
	})

	// One application, and a server that has swap or does not say.
	some := int64(2 << 30)
	f.server.SwapBytes, f.server.UnlimitedMemory = &some, []string{"web"}
	out, _, _ = f.run(t.TempDir(), "doctor")
	assertInOrder(t, out, []string{"! 1 application runs without a memory limit: web.", "No problems; 3 things worth a look."})
	f.server.SwapBytes, f.server.UnlimitedMemory = nil, []string{"a", "b", "c", "d", "e", "f", "g"}
	out, _, _ = f.run(t.TempDir(), "doctor")
	assertInOrder(t, out, []string{"! 7 applications run without a memory limit: a, b, c, d, e and 2 more.", "No problems; 3 things worth a look."})
}

func TestServerStatusSaysHowMuchSwapThereIs(t *testing.T) {
	f := newFakeAgent(t)
	none, some := int64(0), int64(2<<30)
	f.server = api.Server{AgentVersion: "1.2.3", Hostname: "vps-1", MemoryBytes: 4 << 30}
	for _, tc := range []struct {
		swap *int64
		want string
	}{{nil, "4 GB\n"}, {&none, "4 GB, no swap\n"}, {&some, "4 GB, 2 GB swap\n"}} {
		f.server.SwapBytes = tc.swap
		out, _, err := f.run(t.TempDir(), "server", "status")
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(out, tc.want) {
			t.Errorf("swap %v: want a Memory line ending %q in\n%s", tc.swap, tc.want, out)
		}
	}
}

func TestDoctorSaysWhenTheProxyIsNotShipwicks(t *testing.T) {
	f := doctorAgent(t,
		func(string) ([]string, error) { return []string{"203.0.113.10"}, nil },
		func(*http.Request) (*http.Response, error) { return answer(200), nil })
	f.server.Proxy.PlainLookups = true
	out, _, err := f.run(t.TempDir(), "doctor")
	if err != nil {
		t.Fatalf("a proxy that serves is worth a look, not a failure: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"✓ Proxy serving 2 routes",
		"! The proxy is not Shipwick's image of this version",
		"On the server, run the installer again",
		"No problems; 3 things worth a look.",
	})
}

func TestDoctorFailsOnMissingDNSAndCertificates(t *testing.T) {
	f := doctorAgent(t,
		func(host string) ([]string, error) { return nil, notFound(host) },
		func(*http.Request) (*http.Response, error) {
			return nil, &tls.CertificateVerificationError{Err: errors.New("x509: certificate signed by unknown authority")}
		})
	out, _, err := f.run(t.TempDir(), "doctor")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("problems must exit non-zero without a second message, err = %v", err)
	}
	assertInOrder(t, out, []string{
		"✗ api.example.com does not resolve. Create an A record for it pointing at the server, DNS only (not proxied)",
		"✗ www.example.com does not resolve.",
		"✗ https://api.example.com/ has no valid certificate yet: x509: certificate signed by unknown authority",
		"3 problems found.",
	})
}

func TestDoctorSaysWhenTheAgentIsDown(t *testing.T) {
	f := doctorAgent(t, nil, nil)
	f.env = map[string]string{cliconfig.EnvURL: "http://127.0.0.1:1"}
	out, _, err := f.run(t.TempDir(), "doctor")
	if !errors.Is(err, ErrReported) {
		t.Fatalf("err = %v", err)
	}
	assertInOrder(t, out, []string{"✗ The agent at http://127.0.0.1:1 cannot be reached:", "ssh -L 9000:127.0.0.1:9000", "1 problem found."})

	f.env = map[string]string{cliconfig.EnvToken: "wrong"}
	out, _, err = f.run(t.TempDir(), "doctor")
	if !errors.Is(err, ErrReported) || !strings.Contains(out, "✗ The agent rejected the API token. Save a valid one with: shipwick login") {
		t.Errorf("a rejected token: %v\n%s", err, out)
	}
}

func TestDoctorChecksRecordsAgainstTheServersAddress(t *testing.T) {
	var out bytes.Buffer
	c, _ := newRoot(Options{Out: &out, Err: io.Discard, Getenv: func(string) string { return "" }})
	local := localOptions{lookupHost: func(_ context.Context, host string) ([]string, error) {
		switch host {
		case "agent.example.com":
			return []string{"203.0.113.10"}, nil
		case "api.example.com":
			return []string{"104.16.0.1"}, nil
		}
		return nil, notFound(host)
	}}

	r := &report{c: c}
	addrs := c.serverAddresses(context.Background(), r, local, "https://agent.example.com")
	if !reflect.DeepEqual(addrs, []string{"203.0.113.10"}) {
		t.Fatalf("server addresses = %v", addrs)
	}
	c.checkDNS(context.Background(), r, local, "api.example.com", addrs)
	c.checkDNS(context.Background(), r, local, "old.example.com", addrs)
	assertInOrder(t, out.String(), []string{
		"✓ agent.example.com → 203.0.113.10",
		"✗ api.example.com resolves to Cloudflare's proxy (104.16.0.1), not to the server: turn the proxy off for this record (DNS only), or set SHIPWICK_CLOUDFLARE_API_TOKEN on the agent to keep it on",
		"✗ old.example.com does not resolve. Create an A record old.example.com → 203.0.113.10, DNS only (not proxied)",
	})
	if r.problems != 2 {
		t.Errorf("problems = %d", r.problems)
	}
}

func TestDeployWithoutAConfigStartsInit(t *testing.T) {
	f := newFakeAgent(t)
	f.local = localOptions{interactive: func() bool { return true }}
	out, _, err := f.run(t.TempDir(), "deploy")
	// init cannot prompt off a real terminal; that it was reached is the point.
	if err == nil || !strings.Contains(err.Error(), "--image is required") {
		t.Fatalf("init should have been started, err = %v", err)
	}
	if !strings.Contains(out, "No deploy.yaml here. Let's write one.") {
		t.Errorf("unexpected output:\n%s", out)
	}
	if len(f.requests) != 0 {
		t.Errorf("nothing was sent to the agent, saw %v", f.requests)
	}

	// With -f the file is the user's decision, and a missing one is an error.
	f.local = localOptions{interactive: func() bool { return true }}
	out, _, err = f.run(t.TempDir(), "deploy", "-f", "deploy.yaml")
	if got := Render(err); !strings.Contains(got, "deploy.yaml not found") || strings.Contains(out, "Let's write one") {
		t.Errorf("err = %v\n%s", err, out)
	}

	// Off a terminal, as before.
	f.local = localOptions{}
	_, _, err = f.run(t.TempDir(), "deploy")
	if got := Render(err); !strings.Contains(got, "deploy.yaml not found") || !strings.Contains(got, "shipwick init") {
		t.Errorf("unexpected rendering: %s", got)
	}
}

func TestFirstDeploymentEndsWithWhatToDoNext(t *testing.T) {
	polls := func(sequence int) []api.DeploymentDetail {
		done := fixedNow
		return []api.DeploymentDetail{{
			Deployment: api.Deployment{ID: 1, Sequence: sequence, Status: api.StatusActive, Version: "1.4.2", CompletedAt: &done},
			Spec:       spec.App{Domain: "api.example.com"},
		}}
	}
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Replicas: api.ReplicaCount{Desired: 2, Healthy: 2}}}

	f.deploymentPolls = polls(1)
	out, _, err := f.run(writeConfig(t, validConfig), "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"https://api.example.com",
		"Next:",
		"shipwick logs -f my-api", "follow the logs",
		"shipwick status my-api", "replicas, health, history",
	})

	f.deploymentPolls = polls(2)
	out, _, err = f.run(writeConfig(t, validConfig), "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if strings.Contains(out, "Next:") {
		t.Errorf("only the first deployment needs directions:\n%s", out)
	}
}

func TestOpenerArgvKeepsTheURLOneArgument(t *testing.T) {
	url := "https://api.example.com"
	for _, goos := range []string{"windows", "darwin", "linux", "freebsd"} {
		argv := openerArgv(goos, url)
		if argv[len(argv)-1] != url {
			t.Errorf("%s: %q", goos, argv)
		}
	}
}

func TestServerInstallWaitsForADockerThatIsStillStarting(t *testing.T) {
	f := newFakeAgent(t)
	versionCalls, slept := 0, 0
	programs := &fakePrograms{respond: func(argv []string, stdout, stderr io.Writer) error {
		switch remote(argv) {
		case remoteUname:
			fmt.Fprintln(stdout, "Linux x86_64")
		case remoteHasDocker:
		case remoteDockerVersion:
			// The image installed Docker at first boot; the daemon is up on
			// the third look.
			versionCalls++
			if versionCalls < 3 {
				fmt.Fprintln(stderr, "Cannot connect to the Docker daemon at unix:///var/run/docker.sock")
				return errors.New("exit status 1")
			}
			fmt.Fprintln(stdout, "29.8.1")
		default:
			fmt.Fprint(stdout, installerSummary)
		}
		return nil
	}}
	f.local = localOptions{exec: programs.exec, sleep: func(time.Duration) { slept++ },
		lookupHost: func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") }}
	f.env = map[string]string{cliconfig.EnvConfig: filepath.Join(t.TempDir(), "config.yaml")}

	out, _, err := f.run(t.TempDir(), "server", "install", "root@203.0.113.10", "--agent-domain", "agent.example.com")
	if err != nil {
		t.Fatalf("server install: %v\n%s", err, out)
	}
	if versionCalls != 3 || slept != 2 {
		t.Errorf("version asked %d times with %d waits; want 3 and 2", versionCalls, slept)
	}
	assertInOrder(t, out, []string{"Docker is installed but not answering yet; waiting for it to start.", "✓ Docker 29.8.1"})
	for _, argv := range programs.argvs {
		if strings.Contains(remote(argv), "get.docker.com") {
			t.Error("Docker was there; nothing should be installed")
		}
	}
}
