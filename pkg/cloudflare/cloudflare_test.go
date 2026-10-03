package cloudflare

import (
	"net/netip"
	"testing"
	"time"
)

func TestContainsKnowsCloudflaresAddresses(t *testing.T) {
	cases := map[string]bool{
		"104.21.5.6":               true,
		"172.67.1.1":               true,
		"2606:4700:3030::6815:506": true,
		"::ffff:104.21.5.6":        true, // IPv4 mapped into IPv6
		"203.0.113.10":             false,
		"172.63.255.255":           false, // one below 172.64.0.0/13
		"2001:db8::1":              false,
		"":                         false,
		"example.com":              false,
	}
	for ip, want := range cases {
		if got := Contains(ip); got != want {
			t.Errorf("Contains(%q) = %v, want %v", ip, got, want)
		}
	}
}

func TestProxiedNeedsEveryAddressToBeCloudflares(t *testing.T) {
	if !Proxied([]string{"104.21.5.6", "2606:4700:3030::6815:506"}) {
		t.Error("two Cloudflare addresses were not recognised as proxied")
	}
	if Proxied([]string{"104.21.5.6", "198.51.100.7"}) {
		t.Error("a record that also points elsewhere was taken for proxied")
	}
	if Proxied(nil) {
		t.Error("no addresses were taken for proxied")
	}
}

func TestRangesAreValidAndACopy(t *testing.T) {
	got := Ranges()
	if len(got) == 0 {
		t.Fatal("no ranges")
	}
	for _, r := range got {
		if _, err := netip.ParsePrefix(r); err != nil {
			t.Errorf("%q: %v", r, err)
		}
	}
	got[0] = "changed"
	if Ranges()[0] == "changed" {
		t.Error("Ranges hands out its own slice")
	}
	if _, err := time.Parse("2006-01-02", Updated); err != nil {
		t.Errorf("Updated = %q is not a date", Updated)
	}
}
