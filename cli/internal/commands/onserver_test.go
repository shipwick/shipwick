package commands

import (
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func TestOpenDashboardOpensTheAddressTheAgentReports(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{DashboardURL: "https://dashboard.example.com"}
	programs := &fakePrograms{}
	f.local = localOptions{exec: programs.exec, goos: "linux"}

	// In a directory with a deploy.yaml too: the flag decides.
	out, _, err := f.run(writeConfig(t, validConfig), "open", "--dashboard")
	if err != nil {
		t.Fatalf("open --dashboard: %v", err)
	}
	if want := [][]string{{"xdg-open", "https://dashboard.example.com"}}; !reflect.DeepEqual(programs.argvs, want) {
		t.Errorf("ran %q, want %q", programs.argvs, want)
	}
	if !strings.Contains(out, "Opening https://dashboard.example.com") {
		t.Errorf("unexpected output:\n%s", out)
	}

	_, _, err = f.run(t.TempDir(), "open", "--dashboard", "my-api")
	if err == nil || !strings.Contains(err.Error(), "leave the name out") || len(programs.argvs) != 1 {
		t.Errorf("--dashboard with an application is a contradiction: %v, ran %q", err, programs.argvs)
	}
}

func TestOpenDashboardWithoutAHostnameSaysHowToGiveItOne(t *testing.T) {
	f := newFakeAgent(t)
	programs := &fakePrograms{}
	f.local = localOptions{exec: programs.exec}

	_, _, err := f.run(t.TempDir(), "open", "--dashboard")
	want := "This server has no dashboard hostname. Set SHIPWICK_DASHBOARD_DOMAIN in /opt/shipwick/.env and run the installer again"
	if err == nil || Render(err) != "Error: "+want {
		t.Errorf("err = %v", err)
	}
	if len(programs.argvs) != 0 {
		t.Errorf("nothing to open, ran %q", programs.argvs)
	}
}

func TestOpenWithoutAnApplicationOpensTheDashboard(t *testing.T) {
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Domain: "api.example.com"}}
	f.server = api.Server{DashboardURL: "https://dashboard.example.com"}
	programs := &fakePrograms{}
	f.local = localOptions{exec: programs.exec, goos: "linux"}

	if _, _, err := f.run(t.TempDir(), "open"); err != nil {
		t.Fatalf("open: %v", err)
	}
	if _, _, err := f.run(writeConfig(t, validConfig), "open"); err != nil {
		t.Fatalf("open with a deploy.yaml: %v", err)
	}
	want := [][]string{{"xdg-open", "https://dashboard.example.com"}, {"xdg-open", "https://api.example.com"}}
	if !reflect.DeepEqual(programs.argvs, want) {
		t.Errorf("ran %q, want %q", programs.argvs, want)
	}

	// A file that was named and is missing is a mistake, not a wish for
	// the dashboard.
	_, _, err := f.run(t.TempDir(), "open", "-f", "other.yaml")
	if err == nil || !strings.Contains(err.Error(), "other.yaml was not found") || len(programs.argvs) != 2 {
		t.Errorf("err = %v, ran %q", err, programs.argvs)
	}

	// Without a dashboard the question is still which application.
	f.server = api.Server{}
	_, _, err = f.run(t.TempDir(), "open")
	if err == nil || !strings.Contains(err.Error(), "no application given") || len(programs.argvs) != 2 {
		t.Errorf("err = %v, ran %q", err, programs.argvs)
	}
}

func TestServerStatusShowsTheDashboard(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "1.2.3", DashboardURL: "https://dashboard.example.com"}
	out, _, err := f.run(t.TempDir(), "server", "status")
	if err != nil {
		t.Fatalf("server status: %v", err)
	}
	assertInOrder(t, out, []string{"Dashboard", "https://dashboard.example.com"})

	f.server = api.Server{AgentVersion: "1.2.3"}
	out, _, _ = f.run(t.TempDir(), "server", "status")
	assertInOrder(t, out, []string{"Dashboard", "no hostname", "SHIPWICK_DASHBOARD_DOMAIN"})
}

func TestFirstDeploymentNamesTheDashboardWhenThereIsOne(t *testing.T) {
	done := fixedNow
	first := func(app spec.App) []api.DeploymentDetail {
		return []api.DeploymentDetail{{
			Deployment: api.Deployment{ID: 1, Sequence: 1, Status: api.StatusActive, Version: "1.4.2", CompletedAt: &done},
			Spec:       app,
		}}
	}
	f := newFakeAgent(t)
	f.app = api.ApplicationDetail{Application: api.Application{Name: "my-api", Replicas: api.ReplicaCount{Desired: 2, Healthy: 2}}}
	f.server = api.Server{DashboardURL: "https://dashboard.example.com"}

	f.deploymentPolls = first(spec.App{Domain: "api.example.com"})
	out, _, err := f.run(writeConfig(t, validConfig), "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"Next:",
		"  shipwick logs -f my-api          follow the logs\n",
		"  shipwick status my-api           replicas, health, history\n",
		"  https://dashboard.example.com    the dashboard\n",
	})

	// A static application's first line differs; the third does not.
	var printed strings.Builder
	c, _ := newRoot(Options{Out: &printed, Err: &printed, Getenv: func(string) string { return "" }})
	c.printNextSteps("web", true, "https://dashboard.example.com")
	assertInOrder(t, printed.String(), []string{"shipwick open web", "open it in the browser", "shipwick status web", "https://dashboard.example.com", "the dashboard"})

	f.server = api.Server{}
	f.deploymentPolls = first(spec.App{Domain: "api.example.com"})
	out, _, err = f.run(writeConfig(t, validConfig), "deploy")
	if err != nil {
		t.Fatalf("deploy: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Next:") || strings.Contains(out, "dashboard") {
		t.Errorf("no dashboard hostname, no line about it:\n%s", out)
	}
}

func TestLoginNoCheckSavesWithoutAskingTheAgent(t *testing.T) {
	f := newFakeAgent(t)
	path := filepath.Join(t.TempDir(), "config.yaml")
	f.env = map[string]string{cliconfig.EnvConfig: path, cliconfig.EnvURL: "", cliconfig.EnvToken: ""}
	f.stdin = "token-from-the-installer-0123456789\n"

	// A hostname that has no DNS record yet, as on a server just installed.
	out, _, err := f.run(t.TempDir(), "login", "--url", "https://agent.example.invalid", "--token-stdin", "--no-check")
	if err != nil {
		t.Fatalf("login --no-check: %v\n%s", err, out)
	}
	saved, _ := cliconfig.Load(path)
	want := cliconfig.Context{URL: "https://agent.example.invalid", Token: "token-from-the-installer-0123456789"}
	if saved.Current != cliconfig.DefaultContext || saved.Contexts[cliconfig.DefaultContext] != want {
		t.Errorf("unexpected config: %+v", saved)
	}
	if len(f.requests) != 0 {
		t.Errorf("no request was to be made, saw %v", f.requests)
	}
	assertInOrder(t, out, []string{"Saved https://agent.example.invalid as context default", "not checked against the agent"})
	if strings.Contains(out, "token-from-the-installer") {
		t.Errorf("the token must not be echoed:\n%s", out)
	}
}

// The installer reads `shipwick context ls` to find a context that points at
// its own server (sign_in_cli in scripts/install.sh): the URL is the last
// field of a row and the name the first, after the current one's "*".
func TestContextLsKeepsTheShapeTheInstallerReads(t *testing.T) {
	f := newFakeAgent(t)
	withContexts(t, f, "prod")
	out, _, err := f.run(t.TempDir(), "context", "ls")
	if err != nil {
		t.Fatal(err)
	}
	var rows [][]string
	for _, line := range strings.Split(strings.TrimSpace(out), "\n") {
		rows = append(rows, strings.Fields(line))
	}
	want := [][]string{{"NAME", "URL"}, {"*", "prod", "http://127.0.0.1:1"}, {"staging", f.srv.URL}}
	if !reflect.DeepEqual(rows, want) {
		t.Errorf("context ls rows = %q, want %q", rows, want)
	}

	f.env[cliconfig.EnvConfig] = filepath.Join(t.TempDir(), "none.yaml")
	out, _, _ = f.run(t.TempDir(), "context", "ls")
	if !strings.HasPrefix(out, "No saved servers.") {
		t.Errorf("an empty config is announced in words the installer knows:\n%s", out)
	}
}

// onMachine makes Render see a machine with these files and this environment.
func onMachine(t *testing.T, env map[string]string, files ...string) {
	t.Helper()
	before := machine
	t.Cleanup(func() { machine = before })
	machine.getenv = func(k string) string { return env[k] }
	machine.stat = func(name string) (fs.FileInfo, error) {
		for _, f := range files {
			if f == name {
				return nil, nil
			}
		}
		return nil, fs.ErrNotExist
	}
}

func TestOnTheServerAnUnreachableAgentIsNotATunnelAway(t *testing.T) {
	unreachable := func(url string) error {
		return &client.UnreachableError{URL: url, Err: os.ErrDeadlineExceeded}
	}
	const hint = "The agent on this server publishes no port. Give it a hostname (SHIPWICK_AGENT_DOMAIN in /opt/shipwick/.env, then run the installer again), or see \"Reach the API without a hostname\""

	onMachine(t, nil, "/opt/shipwick/.env")
	got := Render(unreachable(cliconfig.DefaultURL))
	if !strings.Contains(got, "cannot reach the Shipwick agent at http://127.0.0.1:9000") || !strings.Contains(got, hint) || strings.Contains(got, "ssh -L") {
		t.Errorf("on the server:\n%s", got)
	}
	// An agent somewhere else is unreachable for other reasons.
	if got := Render(unreachable("https://agent.example.com")); strings.Contains(got, "publishes no port") || !strings.Contains(got, "ssh -L") {
		t.Errorf("another server's agent:\n%s", got)
	}

	onMachine(t, map[string]string{EnvInstallDir: "/srv/shipwick"}, "/srv/shipwick/.env")
	if got := Render(unreachable("http://localhost:9000")); !strings.Contains(got, "SHIPWICK_AGENT_DOMAIN in /srv/shipwick/.env") {
		t.Errorf("an installation elsewhere is named where it is:\n%s", got)
	}

	onMachine(t, nil)
	if got := Render(unreachable(cliconfig.DefaultURL)); strings.Contains(got, "publishes no port") || !strings.Contains(got, "ssh -L") {
		t.Errorf("on a laptop:\n%s", got)
	}
}

func TestAnInstallDirOnlyRootMayReadIsStillAServer(t *testing.T) {
	onMachine(t, nil)
	machine.stat = func(string) (fs.FileInfo, error) { return nil, fs.ErrPermission }
	if installedServer() != "/opt/shipwick/.env" {
		t.Error("permission denied on /opt/shipwick means it exists")
	}
}
