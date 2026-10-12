package commands

import (
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/version"
)

// The CLI under test is a development build; a release version is set for
// the comparison and put back afterwards.
func withCLIVersion(t *testing.T, v string) {
	t.Helper()
	was := version.Version
	version.Version = v
	t.Cleanup(func() { version.Version = was })
}

func TestServerStatusSaysWhichSideToUpgradeWhenTheMinorVersionsDiffer(t *testing.T) {
	tests := map[string]struct {
		cli, agent string
		want       string
	}{
		"a newer CLI":       {"v1.1.0", "v1.0.2", "The CLI (v1.1.0) is newer than the agent (v1.0.2); upgrade the server with: curl -fsSL https://get.shipwick.com | sh"},
		"an older CLI":      {"v1.0.3", "v1.1.0", "The CLI (v1.0.3) is older than the agent (v1.1.0); upgrade it with: shipwick upgrade"},
		"a new major agent": {"v1.4.0", "v2.0.0", "The CLI (v1.4.0) is older than the agent (v2.0.0)"},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			withCLIVersion(t, tt.cli)
			f := newFakeAgent(t)
			f.server = api.Server{AgentVersion: tt.agent}
			out, _, err := f.run(t.TempDir(), "server", "status")
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("no sentence about the versions:\n%s", out)
			}
		})
	}
}

func TestServerStatusSaysNothingOfVersionsThatAgreeOrCannotBeCompared(t *testing.T) {
	tests := map[string]struct{ cli, agent string }{
		"the same release":          {"v1.2.0", "v1.2.0"},
		"patch releases apart":      {"v1.2.3", "v1.2.0"},
		"a development CLI":         {"dev", "v1.2.0"},
		"a CLI built past a tag":    {"v1.2.0-3-gabcdef", "v1.3.0"},
		"an agent that says little": {"v1.2.0", ""},
	}
	for name, tt := range tests {
		t.Run(name, func(t *testing.T) {
			withCLIVersion(t, tt.cli)
			f := newFakeAgent(t)
			f.server = api.Server{AgentVersion: tt.agent}
			out, _, err := f.run(t.TempDir(), "server", "status")
			if err != nil {
				t.Fatal(err)
			}
			if strings.Contains(out, "newer than") || strings.Contains(out, "older than") {
				t.Errorf("a sentence about the versions where none is due:\n%s", out)
			}
		})
	}
}
