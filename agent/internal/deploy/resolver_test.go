package deploy

import (
	"context"
	"errors"
	"net"
	"testing"
)

func TestLookupThroughBelievesTheFirstResolverThatKnowsTheHost(t *testing.T) {
	ctx := context.Background()
	notFound := &net.DNSError{Err: "no such host", Name: "x", IsNotFound: true}
	down := errors.New("dial udp: i/o timeout")
	fallbackCalled := false
	fallback := func(context.Context, string) ([]string, error) {
		fallbackCalled = true
		return []string{"10.0.0.1"}, nil
	}
	answers := func(byServer map[string]any) func(context.Context, string, string) ([]string, error) {
		return func(_ context.Context, server, _ string) ([]string, error) {
			switch v := byServer[server].(type) {
			case []string:
				return v, nil
			case error:
				return nil, v
			}
			return nil, notFound
		}
	}
	servers := []string{"a", "b", "c"}

	tests := []struct {
		name         string
		byServer     map[string]any
		wantAddrs    []string
		wantNotFound bool
		wantFallback bool
	}{
		{"one resolver still has the old negative answer", map[string]any{"a": notFound, "b": []string{"203.0.113.10"}}, []string{"203.0.113.10"}, false, false},
		{"all say no such host", map[string]any{"a": notFound, "b": notFound, "c": notFound}, nil, true, false},
		{"an unreachable resolver is skipped", map[string]any{"a": down, "b": []string{"203.0.113.10"}}, []string{"203.0.113.10"}, false, false},
		{"unreachable and not found: not found wins", map[string]any{"a": down, "b": notFound, "c": down}, nil, true, false},
		{"none reachable: the system resolver decides", map[string]any{"a": down, "b": down, "c": down}, []string{"10.0.0.1"}, false, true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			fallbackCalled = false
			addrs, err := lookupThrough(ctx, "x", servers, answers(tt.byServer), fallback)
			var dnsErr *net.DNSError
			gotNotFound := errors.As(err, &dnsErr) && dnsErr.IsNotFound
			if gotNotFound != tt.wantNotFound || fallbackCalled != tt.wantFallback {
				t.Errorf("notFound=%v fallback=%v err=%v", gotNotFound, fallbackCalled, err)
			}
			if len(addrs) != len(tt.wantAddrs) || (len(addrs) > 0 && addrs[0] != tt.wantAddrs[0]) {
				t.Errorf("addrs = %v, want %v", addrs, tt.wantAddrs)
			}
		})
	}
}

func TestLookupThroughStopsWhenTheContextEnds(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	calls := 0
	_, err := lookupThrough(ctx, "x", []string{"a", "b"}, func(context.Context, string, string) ([]string, error) {
		calls++
		return nil, errors.New("timeout")
	}, nil)
	if !errors.Is(err, context.Canceled) || calls != 1 {
		t.Errorf("err = %v, calls = %d", err, calls)
	}
}
