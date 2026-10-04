package docker

import (
	"net/netip"
	"testing"

	"github.com/moby/moby/api/types/network"
)

func TestControlNetworkIsNamedAfterTheApplicationNetwork(t *testing.T) {
	if got := ControlNetwork("shipwick"); got != "shipwick-control" {
		t.Errorf("ControlNetwork = %q, want shipwick-control", got)
	}
}

func TestGatewayNetworkGoesByPriorityAndThenByName(t *testing.T) {
	cases := []struct {
		name     string
		networks map[string]*network.EndpointSettings
		want     string
	}{
		{"a daemon that kept the agent's lower priority on the application network",
			map[string]*network.EndpointSettings{"shipwick": {GwPriority: selfGwPriority}, "shipwick-control": {}}, "shipwick-control"},
		{"a daemon before 28, which knows no priority: the name that sorts first",
			map[string]*network.EndpointSettings{"shipwick": {}, "shipwick-control": {}}, "shipwick"},
		{"the control network alone", map[string]*network.EndpointSettings{"shipwick-control": {}}, "shipwick-control"},
		{"a higher priority on a network that sorts last",
			map[string]*network.EndpointSettings{"shipwick": {}, "shipwick-control": {}, "zz": {GwPriority: 5}}, "zz"},
		{"a network without an endpoint is no candidate", map[string]*network.EndpointSettings{"a": nil, "shipwick-control": {}}, "shipwick-control"},
		{"no network at all", nil, ""},
	}
	for _, c := range cases {
		if got := gatewayNetwork(c.networks); got != c.want {
			t.Errorf("%s: gatewayNetwork = %q, want %q", c.name, got, c.want)
		}
	}
}

func TestPublishedSeesOnlyTheAPIsOwnPortOnTheServer(t *testing.T) {
	port := func(s string) network.Port {
		p, err := network.ParsePort(s)
		if err != nil {
			t.Fatal(err)
		}
		return p
	}
	loopback := netip.MustParseAddr("127.0.0.1")
	bound := network.PortMap{port("9000/tcp"): {{HostIP: loopback, HostPort: "9000"}}}
	if !published(bound, 9000) {
		t.Error("9000/tcp bound to 127.0.0.1:9000 is published")
	}
	if published(bound, 9001) {
		t.Error("another port of the container is not the API's")
	}
	if published(network.PortMap{port("9000/udp"): {{HostPort: "9000"}}}, 9000) {
		t.Error("the same number over UDP is not the API's port")
	}
	if published(network.PortMap{port("9000/tcp"): nil}, 9000) {
		t.Error("an exposed port without a binding is not published")
	}
	if published(nil, 9000) {
		t.Error("no bindings, nothing published")
	}
}

func TestSubnetsOfSkipsWhatHasNoSubnet(t *testing.T) {
	got := subnetsOf(network.IPAM{Config: []network.IPAMConfig{
		{Subnet: netip.MustParsePrefix("172.20.0.1/16")}, {},
	}})
	if len(got) != 1 || got[0] != netip.MustParsePrefix("172.20.0.0/16") {
		t.Errorf("subnetsOf = %v, want [172.20.0.0/16]", got)
	}
}
