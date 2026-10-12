package commands

import (
	"strings"
	"testing"
)

func TestExpandLeavesTheSplunkTokenForTheServerLikeAnEnvValue(t *testing.T) {
	const config = "name: my-api\nimage: ghcr.io/company/my-api:${TAG}\nport: 8080\nlogging:\n  driver: splunk\n  options:\n    splunk-url: https://hec.example.com:8088\n    splunk-token: ${SPLUNK_TOKEN}\n    tag: ${TAG}\n"

	// Unset here, the token is for the agent to fill in; the tag is not.
	out, used, err := expand([]byte(config), func(n string) (string, bool) { return "1.4.2", n == "TAG" })
	if err != nil {
		t.Fatalf("an unset token must be left for the server: %v", err)
	}
	if strings.Join(used.substituted, ",") != "TAG" || strings.Join(used.deferred, ",") != "SPLUNK_TOKEN" {
		t.Errorf("used = %+v", used)
	}
	if s := string(out); !strings.Contains(s, "splunk-token: ${SPLUNK_TOKEN}") || !strings.Contains(s, "tag: 1.4.2") {
		t.Errorf("out:\n%s", s)
	}

	// Set here, it is filled in like anything else.
	values := map[string]string{"TAG": "1.4.2", "SPLUNK_TOKEN": "hec-1"}
	out, used, err = expand([]byte(config), func(n string) (string, bool) { v, ok := values[n]; return v, ok })
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(out), "splunk-token: hec-1") || len(used.deferred) != 0 {
		t.Errorf("used = %+v\n%s", used, out)
	}
}
