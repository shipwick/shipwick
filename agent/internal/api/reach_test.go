package api

import (
	"context"
	"encoding/json"
	"errors"
	"net"
	"net/http"
	"net/http/httptest"
	"net/netip"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

// The addresses of an installation as the tests see it: two application
// networks, the control network, a replica on both of the first, the proxy
// on all three.
const (
	replicaAddr        = "172.18.0.5"
	replicaServiceAddr = "172.19.0.5"
	proxyAppAddr       = "172.18.0.3"
	proxyControlAddr   = "172.20.0.3"
	agentControlAddr   = "172.20.0.2"
	dockerBridgeAddr   = "172.17.0.1"
)

func testOrigins(calls *int) func(context.Context) (Origins, error) {
	return func(context.Context) (Origins, error) {
		*calls++
		return Origins{
			Subnets:    []netip.Prefix{netip.MustParsePrefix("172.18.0.0/16"), netip.MustParsePrefix("172.19.0.0/16")},
			Containers: []netip.Addr{netip.MustParseAddr(replicaAddr), netip.MustParseAddr(replicaServiceAddr)},
		}, nil
	}
}

// arriving sends a request from a client at remote that reached the agent at
// its address local; an empty local is a listener the test says nothing about.
func (f *fixture) arriving(h http.Handler, remote, local, path, auth string) *httptest.ResponseRecorder {
	f.t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	req.RemoteAddr = net.JoinHostPort(remote, "40000")
	if local != "" {
		req = req.WithContext(context.WithValue(req.Context(), http.LocalAddrContextKey, &net.TCPAddr{IP: net.ParseIP(local), Port: 9000}))
	}
	if auth != "" {
		req.Header.Set("Authorization", auth)
	}
	rec := httptest.NewRecorder()
	h.ServeHTTP(rec, req)
	return rec
}

func assertRefused(t *testing.T, what string, rec *httptest.ResponseRecorder) {
	t.Helper()
	if rec.Code != http.StatusForbidden {
		t.Errorf("%s: status = %d, want 403", what, rec.Code)
		return
	}
	if e := decodeError(t, rec.Body.Bytes()); e.Code != api.CodeApplicationCaller {
		t.Errorf("%s: code = %q, want %s", what, e.Code, api.CodeApplicationCaller)
	}
}

func TestAManagedContainerIsRefusedWhateverTokenItCarries(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.api.UseReach(Reach{Open: true, Origins: testOrigins(&calls)})
	h := f.api.Handler()
	const good = "Bearer " + testToken

	assertRefused(t, "a replica with the right token", f.from(h, replicaAddr, "/api/v1/applications", good))
	assertRefused(t, "a replica at its address on the services network", f.from(h, replicaServiceAddr, "/api/v1/applications", good))
	assertRefused(t, "a replica asking for the unauthenticated endpoint", f.from(h, replicaAddr, "/api/v1/health", ""))

	// Without the control network the proxy and the dashboard call from the
	// application network too, and must keep working.
	if rec := f.from(h, proxyAppAddr, "/api/v1/applications", good); rec.Code != http.StatusOK {
		t.Errorf("the proxy on the application network: status = %d, want 200", rec.Code)
	}
	if rec := f.from(h, "127.0.0.1", "/api/v1/applications", good); rec.Code != http.StatusOK {
		t.Errorf("loopback: status = %d, want 200", rec.Code)
	}
}

func TestARefusedContainerIsNotCountedAsAFailedToken(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.api.UseReach(Reach{Open: true, Origins: testOrigins(&calls)})
	clock := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	h := f.api.Handler()

	for i := range failedAuthLimit + 5 {
		clock = clock.Add(time.Second)
		assertRefused(t, "a guess from a replica", f.from(h, replicaAddr, "/api/v1/applications", "Bearer guess-"+string(rune('a'+i))))
	}
	// The replica is removed and something else takes its address: it must
	// not find itself locked out for what the replica tried.
	f.api.UseReach(Reach{})
	if rec := f.from(h, replicaAddr, "/api/v1/applications", "Bearer wrong"); rec.Code != http.StatusUnauthorized {
		t.Errorf("the next holder of the address, with a wrong token: status = %d, want 401, not 429", rec.Code)
	}
}

func TestOnTheControlNetworkOnlyItsOwnAddressesAreAnswered(t *testing.T) {
	f := newFixture(t)
	calls := 0
	f.api.UseReach(Reach{
		Control:        netip.MustParseAddr(agentControlAddr),
		ControlSubnets: []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")},
		Closed:         true,
		Origins:        testOrigins(&calls),
	})
	h := f.api.Handler()
	const good = "Bearer " + testToken

	if rec := f.arriving(h, proxyControlAddr, agentControlAddr, "/api/v1/applications", good); rec.Code != http.StatusOK {
		t.Errorf("the proxy from the control network: status = %d, want 200", rec.Code)
	}
	// The server itself, through a published port, comes from the bridge.
	if rec := f.arriving(h, "172.20.0.1", agentControlAddr, "/api/v1/applications", good); rec.Code != http.StatusOK {
		t.Errorf("the control network's gateway: status = %d, want 200", rec.Code)
	}
	// What a container achieves by sending to the agent's interface on the
	// application network a packet addressed to the control one.
	assertRefused(t, "an application address arriving at the control address", f.arriving(h, replicaAddr, agentControlAddr, "/api/v1/applications", good))
	assertRefused(t, "the proxy's application address arriving at the control address", f.arriving(h, proxyAppAddr, agentControlAddr, "/api/v1/applications", good))
	assertRefused(t, "an address of no network of the installation", f.arriving(h, "10.9.8.7", agentControlAddr, "/api/v1/applications", good))
	if calls != 0 {
		t.Errorf("the runtime was asked %d times; the control network needs no list of containers", calls)
	}
}

func TestWithTheControlNetworkEveryApplicationAddressIsRefusedElsewhere(t *testing.T) {
	// An agent on the host that listens on Docker's bridge: applications
	// reach that address, and one can claim an address the agent does not
	// know, the proxy's on the application network.
	f := newFixture(t)
	calls := 0
	f.api.UseReach(Reach{
		Control:        netip.MustParseAddr("172.20.0.1"),
		ControlSubnets: []netip.Prefix{netip.MustParsePrefix("172.20.0.0/16")},
		Closed:         true,
		Origins:        testOrigins(&calls),
	})
	h := f.api.Handler()
	const good = "Bearer " + testToken

	assertRefused(t, "a replica", f.arriving(h, replicaAddr, dockerBridgeAddr, "/api/v1/applications", good))
	assertRefused(t, "the proxy's address on the application network", f.arriving(h, proxyAppAddr, dockerBridgeAddr, "/api/v1/applications", good))
	assertRefused(t, "an address of the application network nobody has", f.arriving(h, "172.18.0.77", dockerBridgeAddr, "/api/v1/applications", good))
	if rec := f.arriving(h, "172.20.0.4", dockerBridgeAddr, "/api/v1/applications", good); rec.Code != http.StatusOK {
		t.Errorf("the dashboard from the control network: status = %d, want 200", rec.Code)
	}
}

func TestTheRuntimeIsAskedAboutContainersAtMostEveryTwoSeconds(t *testing.T) {
	f := newFixture(t)
	calls := 0
	containers := []netip.Addr{}
	f.api.UseReach(Reach{Open: true, Origins: func(context.Context) (Origins, error) {
		calls++
		return Origins{Containers: containers}, nil
	}})
	clock := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	h := f.api.Handler()
	const good = "Bearer " + testToken

	for range 10 {
		if rec := f.from(h, replicaAddr, "/api/v1/applications", good); rec.Code != http.StatusOK {
			t.Fatalf("an address no container has: status = %d, want 200", rec.Code)
		}
	}
	if calls != 1 {
		t.Errorf("the runtime was asked %d times for ten requests in one instant, want once", calls)
	}
	// A loopback caller never causes a question.
	clock = clock.Add(time.Minute)
	f.from(h, "127.0.0.1", "/api/v1/applications", good)
	if calls != 1 {
		t.Errorf("a request from loopback asked the runtime")
	}

	// A container that starts is refused once the answer is old enough.
	containers = []netip.Addr{netip.MustParseAddr(replicaAddr)}
	assertRefused(t, "a container started since the last answer", f.from(h, replicaAddr, "/api/v1/applications", good))
	if calls != 2 {
		t.Errorf("the runtime was asked %d times, want 2", calls)
	}
}

func TestARuntimeThatDoesNotAnswerLeavesTheLastAnswerInPlace(t *testing.T) {
	f := newFixture(t)
	down := false
	f.api.UseReach(Reach{Open: true, Origins: func(context.Context) (Origins, error) {
		if down {
			return Origins{}, errors.New("docker daemon unreachable")
		}
		return Origins{Containers: []netip.Addr{netip.MustParseAddr(replicaAddr)}}, nil
	}})
	clock := time.Date(2026, 10, 4, 12, 0, 0, 0, time.UTC)
	f.api.now = func() time.Time { return clock }
	h := f.api.Handler()

	assertRefused(t, "a replica", f.from(h, replicaAddr, "/api/v1/health", ""))
	down = true
	clock = clock.Add(time.Minute)
	assertRefused(t, "a replica while Docker does not answer", f.from(h, replicaAddr, "/api/v1/health", ""))
	if rec := f.from(h, proxyAppAddr, "/api/v1/health", ""); rec.Code != http.StatusOK {
		t.Errorf("the proxy while Docker does not answer: status = %d, want 200", rec.Code)
	}
	if !strings.Contains(f.logs.String(), "could not ask Docker which addresses belong to applications") {
		t.Errorf("the log does not say that Docker could not be asked:\n%s", f.logs.String())
	}
}

func TestServerSaysWhenApplicationsCanReachTheAPI(t *testing.T) {
	f := newFixture(t)
	field := func() (bool, bool) {
		t.Helper()
		status, body := f.do(http.MethodGet, "/api/v1/server", "")
		if status != http.StatusOK {
			t.Fatalf("GET /server: status = %d", status)
		}
		var got struct {
			Data map[string]json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(body, &got); err != nil {
			t.Fatal(err)
		}
		raw, present := got.Data["open_to_applications"]
		return present, string(raw) == "true"
	}

	if present, _ := field(); present {
		t.Error("open_to_applications is present for an agent that was told nothing; it is present only when true")
	}
	f.api.UseReach(Reach{Closed: true})
	if present, _ := field(); present {
		t.Error("open_to_applications is present for an agent on the control network")
	}
	f.api.UseReach(Reach{Open: true})
	if present, open := field(); !present || !open {
		t.Errorf("open_to_applications present = %v, value = %v; want true for an agent that listens where applications are", present, open)
	}
}
