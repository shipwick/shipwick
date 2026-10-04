package commands

import (
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const securedConfig = `name: api
image: ghcr.io/company/api:1.4.2
port: 8080
user: "1000:1000"
security:
  read_only: true
  tmpfs:
    - /tmp
    - path: /var/cache/api
      size: 200mb
  capabilities: none
  non_root: true
`

func TestValidateShowsWhatTheSecurityBlockTakesAway(t *testing.T) {
	f := newFakeAgent(t)
	out, _, err := f.run(writeConfig(t, securedConfig), "validate")
	if err != nil {
		t.Fatalf("validate: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Security", "read-only root filesystem; tmpfs at /tmp (64 MB), /var/cache/api (200 MB); no capabilities; refuses to run as root"})

	kept := strings.Replace(securedConfig, "capabilities: none", "capabilities: [SETUID, CHOWN]", 1)
	if out, _, err = f.run(writeConfig(t, kept), "validate"); err != nil || !strings.Contains(out, "capabilities CHOWN, SETUID only") {
		t.Errorf("two capabilities kept: %v\n%s", err, out)
	}

	plain, _, err := f.run(writeConfig(t, validConfig), "validate")
	if err != nil || strings.Contains(plain, "Security") {
		t.Errorf("an application without the block has a Security line (%v):\n%s", err, plain)
	}

	_, _, err = f.run(writeConfig(t, strings.Replace(securedConfig, `user: "1000:1000"`, "user: root", 1)), "validate")
	if got := Render(err); !strings.Contains(got, "user:") || !strings.Contains(got, "next to security.non_root") {
		t.Errorf("root next to non_root renders as:\n%s", got)
	}
}

// An agent from before a key answers it as any key it does not know. That is
// a refusal, so nothing runs unprotected; what it lacks is the reason.
func TestAnAgentOlderThanAKeyIsNamedAsTheReasonItRefusesIt(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "0.7.0", Hostname: "vps-1"}
	f.deployStatus = 400
	f.deployError = api.Error{Code: api.CodeInvalidConfig, Message: "invalid deploy.yaml", Details: map[string]any{
		"fields": []map[string]string{{"field": "line 5", "message": `unknown field "security"`, "expected": ""}},
	}}
	_, _, err := f.run(writeConfig(t, securedConfig), "deploy")
	got := Render(err)
	for _, want := range []string{
		"invalid deploy.yaml\n\nline 5:\n  unknown field \"security\"\n",
		`The agent is version 0.7.0 and does not know "security"`,
		"Nothing was deployed.",
		installerCommand + " (on the server)",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("the refusal renders as:\n%s\nwant it to hold %q", got, want)
		}
	}
	if strings.HasPrefix(got, "Error:") {
		t.Errorf("the report lost its form:\n%s", got)
	}

	// Any other refusal of the agent's stays as it was.
	f.deployError = api.Error{Code: api.CodeInvalidConfig, Message: "invalid deploy.yaml", Details: map[string]any{
		"fields": []map[string]string{{"field": "domain", "message": "is served by another application", "expected": ""}},
	}}
	_, _, err = f.run(writeConfig(t, securedConfig), "deploy")
	if got := Render(err); strings.Contains(got, "does not know") || !strings.Contains(got, "is served by another application") {
		t.Errorf("a refusal that is no unknown key renders as:\n%s", got)
	}
}

func TestInitShowsTheSecurityBlockAsAnExampleThatIsValid(t *testing.T) {
	f := newFakeAgent(t)
	dir := t.TempDir()
	if _, _, err := f.run(dir, "init", "--name", "app", "--image", "ghcr.io/company/app:1.0.0", "--port", "8080"); err != nil {
		t.Fatalf("init: %v", err)
	}
	content := readOrEmpty(t, dir, DefaultFile)
	app, err := spec.Parse([]byte(content))
	if err != nil {
		t.Fatal(err)
	}
	if app.Security != nil || !strings.Contains(content, "\n# security:\n#   read_only: true\n") {
		t.Fatalf("security should be there as an example only:\n%s", content)
	}

	// Uncommented, the example is a block the agent accepts.
	var block []string
	in := false
	for _, line := range strings.Split(content, "\n") {
		switch {
		case line == "# security:":
			in = true
		case in && !strings.HasPrefix(line, "#   "):
			in = false
		}
		if in {
			block = append(block, strings.TrimPrefix(line, "# "))
		}
	}
	app, err = spec.Parse([]byte("name: app\nimage: app:1\nuser: \"1000\"\n" + strings.Join(block, "\n") + "\n"))
	if err != nil {
		t.Fatalf("the example does not parse: %v\n%s", err, strings.Join(block, "\n"))
	}
	if s := app.Security; s == nil || !s.ReadOnly || !s.NonRoot || len(s.Tmpfs) != 1 || s.Capabilities == nil || len(*s.Capabilities) != 0 {
		t.Errorf("the example says %+v", app.Security)
	}
}
