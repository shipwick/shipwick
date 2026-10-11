package commands

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"errors"
	"fmt"
	"net"
	"net/url"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

// A firewall that drops packets is silence, and the only honest way to
// produce silence is to dial an address nothing routes: 10.255.255.1 is
// private and unassigned on any network this is likely to run on. The
// deadline is the context's, so the test ends when it says, not after the
// dialer's three seconds.
func TestASilentAddressIsATimeout(t *testing.T) {
	local := localOptions{}.withDefaults()
	ctx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	err := local.dial(ctx, "10.255.255.1:443")
	if err == nil {
		t.Skip("10.255.255.1:443 answered; this network has something there")
	}
	if errors.Is(err, syscall.ENETUNREACH) || errors.Is(err, syscall.EHOSTUNREACH) {
		t.Skip("this machine has no route to 10.0.0.0/8 at all")
	}
	if got := classifyDial(err); got != portTimedOut {
		t.Errorf("classifyDial(%v) = %d, want portTimedOut", err, got)
	}
}

// A port nothing listens on is refused by the kernel, at once.
func TestAClosedPortIsARefusal(t *testing.T) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	address := l.Addr().String()
	l.Close()
	local := localOptions{}.withDefaults()
	err = local.dial(context.Background(), address)
	if err == nil {
		t.Fatalf("%s accepted a connection after the listener closed", address)
	}
	if got := classifyDial(err); got != portRefused {
		t.Errorf("classifyDial(%v) = %d, want portRefused", err, got)
	}
}

func TestClassifyDialReadsTheErrorNotTheText(t *testing.T) {
	timeout := &net.OpError{Op: "dial", Err: &timeoutErr{}}
	if classifyDial(timeout) != portTimedOut || classifyDial(context.DeadlineExceeded) != portTimedOut {
		t.Error("a net.Error that says Timeout, or the context's deadline, is a timeout")
	}
	if classifyDial(&net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}) != portRefused {
		t.Error("ECONNREFUSED is a refusal")
	}
	if classifyDial(errors.New("no route to host")) != portUnreachable || classifyDial(nil) != portOpen {
		t.Error("anything else is neither, and no error is open")
	}
}

type timeoutErr struct{}

func (*timeoutErr) Error() string   { return "i/o timeout" }
func (*timeoutErr) Timeout() bool   { return true }
func (*timeoutErr) Temporary() bool { return true }

var (
	silence = &net.OpError{Op: "dial", Err: &timeoutErr{}}
	refused = &net.OpError{Op: "dial", Err: os.NewSyscallError("connect", syscall.ECONNREFUSED)}
)

// dialing answers each port as the test says; the map is by port.
func dialing(states map[string]error) func(context.Context, string) error {
	return func(_ context.Context, address string) error {
		_, port, _ := net.SplitHostPort(address)
		return states[port]
	}
}

// doctor's port lines, told apart: silence is the firewall in front of the
// server, a refusal is the server itself with nothing listening.
func TestDoctorTellsASilentPortFromAClosedOne(t *testing.T) {
	var printed strings.Builder
	c, _ := newRoot(Options{Out: &printed, Err: &printed, Getenv: func(string) string { return "" }})
	r := &report{c: c}
	local := localOptions{dial: dialing(map[string]error{"80": silence, "443": refused})}.withDefaults()
	for _, port := range []int{80, 443} {
		probePort(context.Background(), local, r, "203.0.113.10", port)
	}
	assertInOrder(t, printed.String(), []string{
		"✗ Port 80 on 203.0.113.10 timed out after 3s: a firewall in front of the server drops the packets — the provider's firewall (Hetzner Cloud Firewall, an AWS security group",
		"Open 80 and 443 TCP and 443 UDP there; certificates cannot be obtained until then",
		"✗ Port 443 on 203.0.113.10 refused the connection: nothing listens there. The proxy is not up; on the server: cd /opt/shipwick && docker compose ps",
	})
	if r.problems != 2 {
		t.Errorf("problems = %d", r.problems)
	}

	printed.Reset()
	r = &report{c: c}
	local.dial = dialing(map[string]error{"443": errors.New("dial tcp: no route to host")})
	for _, port := range []int{80, 443} {
		probePort(context.Background(), local, r, "203.0.113.10", port)
	}
	assertInOrder(t, printed.String(), []string{
		"✓ Port 80 open on 203.0.113.10",
		"✗ Port 443 on 203.0.113.10 could not be reached: dial tcp: no route to host. Check the address, and the route from this machine to it",
	})
}

// doctor's first line, for an agent that did not answer at all.
func TestDoctorTellsATimeoutFromARefusalFromATLSFailure(t *testing.T) {
	wrap := func(err error) error {
		return &url.Error{Op: "Get", URL: "https://agent.example.com/api/v1/health", Err: err}
	}
	cases := []struct {
		name string
		url  string
		err  error
		want []string
		not  string
	}{
		{"timeout", "https://agent.example.com", wrap(silence),
			[]string{"The agent at https://agent.example.com did not answer within 10s", "a firewall in front of the server drops the packets", "Hetzner Cloud Firewall", "443 UDP"}, "Is it running?"},
		{"refused", "https://agent.example.com", wrap(refused),
			[]string{"The agent at https://agent.example.com refused the connection: nothing listens on that port", "docker compose ps"}, "firewall"},
		{"refused on this machine", "http://127.0.0.1:9000", wrap(refused),
			[]string{"refused the connection: nothing listens there", "ssh -L 9000:127.0.0.1:9000"}, "docker compose ps"},
		{"certificate", "https://agent.example.com", wrap(&tls.CertificateVerificationError{Err: x509.UnknownAuthorityError{}}),
			[]string{"The agent at https://agent.example.com answered, but the TLS handshake failed", "no certificate for this hostname yet", "docker logs shipwick-caddy-1"}, "firewall in front"},
		{"alert", "https://agent.example.com", wrap(tls.AlertError(80)),
			[]string{"TLS handshake failed: tls: internal error"}, "firewall in front"},
		{"other", "https://agent.example.com", wrap(errors.New("EOF")),
			[]string{"The agent at https://agent.example.com cannot be reached: EOF. Is it running?"}, "firewall"},
	}
	for _, tc := range cases {
		got := unreachableAgent(tc.url, tc.url, tc.err, healthTimeout)
		for _, want := range tc.want {
			if !strings.Contains(got, want) {
				t.Errorf("%s: missing %q in:\n%s", tc.name, want, got)
			}
		}
		if strings.Contains(got, tc.not) {
			t.Errorf("%s: %q does not belong in:\n%s", tc.name, tc.not, got)
		}
	}
}

func TestServerInstallProbesThePortsWhenTheInstallerIsDone(t *testing.T) {
	f := newFakeAgent(t)
	programs := &fakePrograms{respond: freshServer}
	f.local = localOptions{exec: programs.exec,
		lookupHost: func(context.Context, string) ([]string, error) { return nil, errors.New("no such host") },
		dial:       dialing(map[string]error{"80": silence, "443": silence}),
	}
	out, _, err := f.run(t.TempDir(), "server", "install", "root@203.0.113.10", "--agent-domain", "agent.example.com", "--dashboard-domain", "dashboard.example.com")
	if err != nil {
		t.Fatalf("a closed port after a successful installation is said, not failed on: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"✓ Shipwick is running on root@203.0.113.10",
		"✓ Saved the API token",
		"✗ Port 80 on 203.0.113.10 timed out after 3s: a firewall in front of the server drops the packets",
		"✗ Port 443 on 203.0.113.10 timed out",
		"Once the ports are open, Caddy obtains the certificates by itself; check with: shipwick doctor",
		"Create these DNS records",
		"Dashboard: https://dashboard.example.com",
		"It asks for a token; make it one of its own: shipwick token create dashboard --role admin",
		"not the root token from /opt/shipwick/.env",
		"Next: in your project, run: shipwick init",
	})

	// Open ports, and a hostname for a target: the probe goes to the address.
	var dialled []string
	f.local.lookupHost = func(_ context.Context, host string) ([]string, error) {
		if host == "box.example.net" {
			return []string{"203.0.113.10"}, nil
		}
		return nil, errors.New("no such host")
	}
	f.local.dial = func(_ context.Context, address string) error { dialled = append(dialled, address); return nil }
	out, _, err = f.run(t.TempDir(), "server", "install", "root@box.example.net")
	if err != nil {
		t.Fatalf("server install: %v\n%s", err, out)
	}
	if want := []string{"203.0.113.10:80", "203.0.113.10:443"}; fmt.Sprint(dialled) != fmt.Sprint(want) {
		t.Errorf("dialled %q, want %q", dialled, want)
	}
	assertInOrder(t, out, []string{"✓ Port 80 open on 203.0.113.10", "✓ Port 443 open on 203.0.113.10"})
	if strings.Contains(out, "It asks for a token") {
		t.Errorf("no dashboard hostname, no sign-in line:\n%s", out)
	}
}
