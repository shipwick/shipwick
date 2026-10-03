package deploy

import (
	"context"
	"errors"
	"net"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

func TestResolversThatCannotBeReachedAreLeftAloneForAWhile(t *testing.T) {
	now := time.Date(2026, 10, 3, 12, 0, 0, 0, time.UTC)
	asked, reachable := 0, false
	r := &Resolvers{
		servers: []string{"a", "b", "c"},
		ask: func(context.Context, string, string) ([]string, error) {
			asked++
			if reachable {
				return []string{"203.0.113.10"}, nil
			}
			return nil, errors.New("read udp: i/o timeout")
		},
		fallback: func(context.Context, string) ([]string, error) { return []string{"10.0.0.1"}, nil },
		now:      func() time.Time { return now },
	}
	ctx := context.Background()

	addrs, err := r.LookupHost(ctx, "app.example.internal")
	if err != nil || len(addrs) != 1 || addrs[0] != "10.0.0.1" || asked != 3 {
		t.Fatalf("first lookup: %v, %v after %d questions; want the system's answer after three", addrs, err, asked)
	}
	now = now.Add(resolversSilentFor - time.Second)
	if addrs, _ = r.LookupHost(ctx, "other.example.internal"); asked != 3 || addrs[0] != "10.0.0.1" {
		t.Errorf("a lookup soon after asked the silent servers again (%d questions)", asked)
	}
	now = now.Add(2 * time.Second)
	reachable = true
	if addrs, _ = r.LookupHost(ctx, "app.example.com"); asked != 4 || addrs[0] != "203.0.113.10" {
		t.Errorf("after %v the servers were not asked again: %v, %d questions", resolversSilentFor, addrs, asked)
	}
}

func TestResolversThatAnswerNoSuchHostAreNotSilent(t *testing.T) {
	asked := 0
	r := &Resolvers{
		servers: []string{"a"},
		ask: func(context.Context, string, string) ([]string, error) {
			asked++
			return nil, &net.DNSError{Err: "no such host", IsNotFound: true}
		},
		fallback: func(context.Context, string) ([]string, error) {
			t.Error("the system's resolver was asked about a hostname the servers know not to exist")
			return nil, nil
		},
		now: time.Now,
	}
	r.LookupHost(context.Background(), "x.example.com")
	r.LookupHost(context.Background(), "x.example.com")
	if asked != 2 {
		t.Errorf("asked %d times, want 2", asked)
	}
}

func TestNetworkStatusPutsTheDaemonsProxyNextToTheAgents(t *testing.T) {
	e := &Engine{opts: Options{Network: NetworkOptions{Proxy: "proxy.example.com:3128", CAFile: true}}}
	got := e.networkStatus(docker.Info{Proxy: false})
	if got.Proxy != "proxy.example.com:3128" || got.DockerProxy || !got.CAFile {
		t.Errorf("status = %+v", got)
	}
	if got.DNSResolvers == nil {
		t.Error("dns_resolvers would be null in the API; clients expect a list")
	}
	if got = e.networkStatus(docker.Info{Proxy: true}); !got.DockerProxy {
		t.Error("the daemon's proxy is not reported")
	}
}
