package proxy

import (
	"encoding/json"
	"strings"
	"testing"
)

const testDirectory = "https://ca.example.internal/acme/acme/directory"

// issuers returns the issuers of the one automation policy in a
// configuration, as Caddy reads them.
func issuers(t *testing.T, tls TLS) []map[string]any {
	t.Helper()
	_, _, raw := buildTLS(t, someRoutes, tls)
	var c struct {
		Apps struct {
			TLS struct {
				Automation struct {
					Policies []struct {
						Subjects []string
						Issuers  []map[string]any
					}
				}
			}
		}
	}
	if err := json.Unmarshal(raw, &c); err != nil {
		t.Fatal(err)
	}
	policies := c.Apps.TLS.Automation.Policies
	if len(policies) != 1 || len(policies[0].Subjects) != 0 {
		t.Fatalf("policies = %+v, want one that applies to every hostname", policies)
	}
	return policies[0].Issuers
}

func TestAnACMEDirectoryReplacesTheAuthoritiesCaddyAsksByDefault(t *testing.T) {
	got := issuers(t, TLS{ACMEDirectory: testDirectory})
	if len(got) != 1 || got[0]["module"] != "acme" || got[0]["ca"] != testDirectory {
		t.Fatalf("issuers = %v, want the one ACME issuer at the directory", got)
	}
	if _, challenges := got[0]["challenges"]; challenges {
		t.Errorf("a DNS challenge nobody configured: %v", got[0])
	}
}

func TestAnACMEDirectoryAndACloudflareTokenAreOneIssuer(t *testing.T) {
	got := issuers(t, TLS{ACMEDirectory: testDirectory, CloudflareToken: testToken})
	if len(got) != 1 || got[0]["ca"] != testDirectory || got[0]["challenges"] == nil {
		t.Fatalf("issuers = %v", got)
	}
	if only := issuers(t, TLS{CloudflareToken: testToken}); only[0]["ca"] != nil {
		t.Errorf("a token alone names a directory: %v", only[0])
	}
}

func TestRequestsToReplicasNeverGoThroughTheProxyOfCaddysEnvironment(t *testing.T) {
	_, _, raw := buildTLS(t, someRoutes, TLS{})
	transports := strings.Count(string(raw), `"transport":`)
	if transports == 0 || strings.Count(string(raw), `"network_proxy":{"from":"none"}`) != transports {
		t.Errorf("%d reverse-proxy transports, not every one with network_proxy none:\n%s", transports, raw)
	}
}
