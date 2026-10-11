package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"regexp"
	"strings"
	"testing"
)

// fakeRemote stands in for ssh running the configure script: it keeps the
// argv and what arrived on standard input, and answers with the report the
// test gives it.
type fakeRemote struct {
	argvs  [][]string
	inputs []string
	report string
	stderr string
	err    error
}

func (r *fakeRemote) exec(_ context.Context, argv []string, stdin io.Reader, stdout, stderr io.Writer) error {
	r.argvs = append(r.argvs, argv)
	in, _ := io.ReadAll(stdin)
	r.inputs = append(r.inputs, string(in))
	fmt.Fprint(stdout, r.report)
	fmt.Fprint(stderr, r.stderr)
	return r.err
}

func TestServerConfigureSendsValuesAsDataAndNamesWhatChanged(t *testing.T) {
	f := newFakeAgent(t)
	remote := &fakeRemote{report: "changed SHIPWICK_WEBHOOK_URL\nadded SHIPWICK_WEBHOOK_SECRET\nremoved SHIPWICK_UPDATE_CHECK\nunchanged SHIPWICK_ALERT_DISK_PERCENT\nabsent SHIPWICK_CA_FILE\nrestarting\nhealthy\n",
		stderr: " Container shipwick-agent-1  Recreated\n"}
	f.local = localOptions{execInput: remote.exec}

	out, _, err := f.run(t.TempDir(), "server", "configure", "root@203.0.113.10",
		"SHIPWICK_WEBHOOK_URL=https://hooks.example.com/a?b=c&d='e'", "SHIPWICK_WEBHOOK_SECRET=s3cret $(id) `x`",
		"SHIPWICK_ALERT_DISK_PERCENT=85", "--unset", "SHIPWICK_UPDATE_CHECK", "--unset", "SHIPWICK_CA_FILE")
	if err != nil {
		t.Fatalf("server configure: %v\n%s", err, out)
	}

	// One ssh, running the fixed script and nothing of the arguments.
	if len(remote.argvs) != 1 || !reflect.DeepEqual(remote.argvs[0], sshArgv(sshTarget{"root", "203.0.113.10"}, remoteConfigure)) {
		t.Errorf("ssh was run as %q", remote.argvs)
	}
	for _, argv := range remote.argvs {
		if strings.Contains(strings.Join(argv, " "), "s3cret") || strings.Contains(strings.Join(argv, " "), "hooks.example.com") {
			t.Errorf("a value reached the command line: %q", argv)
		}
	}
	// The values travel on standard input, in the file's own format, in the
	// order given; a removal is an empty value.
	want := "SHIPWICK_WEBHOOK_URL=https://hooks.example.com/a?b=c&d='e'\nSHIPWICK_WEBHOOK_SECRET=s3cret $(id) `x`\nSHIPWICK_ALERT_DISK_PERCENT=85\nSHIPWICK_UPDATE_CHECK=\nSHIPWICK_CA_FILE=\n"
	if remote.inputs[0] != want {
		t.Errorf("stdin was\n%q\nwant\n%q", remote.inputs[0], want)
	}

	assertInOrder(t, out, []string{
		"  " + " Container shipwick-agent-1  Recreated",
		"✓ /opt/shipwick/.env on root@203.0.113.10: SHIPWICK_WEBHOOK_URL changed, SHIPWICK_WEBHOOK_SECRET added, SHIPWICK_UPDATE_CHECK removed",
		"SHIPWICK_ALERT_DISK_PERCENT: already that value",
		"SHIPWICK_CA_FILE: not in the file",
		"✓ The agent is healthy",
	})
	if strings.Contains(out, "s3cret") || strings.Contains(out, "hooks.example.com") {
		t.Errorf("a value was printed:\n%s", out)
	}
}

func TestServerConfigureSaysWhenNothingChangedAndWhatComesNext(t *testing.T) {
	f := newFakeAgent(t)
	remote := &fakeRemote{report: "unchanged SHIPWICK_WEBHOOK_URL\n"}
	f.local = localOptions{execInput: remote.exec}
	out, _, err := f.run(t.TempDir(), "server", "configure", "root@203.0.113.10", "SHIPWICK_WEBHOOK_URL=https://hooks.example.com/")
	if err != nil {
		t.Fatalf("server configure: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"✓ Nothing to change in /opt/shipwick/.env on root@203.0.113.10; the agent was not restarted", "SHIPWICK_WEBHOOK_URL: already that value"})
	if strings.Contains(out, "healthy") {
		t.Errorf("no restart, no health:\n%s", out)
	}

	remote.report = "added SHIPWICK_CLOUDFLARE_API_TOKEN\nchanged SHIPWICK_AGENT_DOMAIN\nrestarting\nhealthy\n"
	out, _, err = f.run(t.TempDir(), "server", "configure", "root@203.0.113.10", "SHIPWICK_CLOUDFLARE_API_TOKEN=abc_DEF-123", "SHIPWICK_AGENT_DOMAIN=agent.example.com")
	if err != nil {
		t.Fatalf("server configure: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{
		"✓ /opt/shipwick/.env on root@203.0.113.10: SHIPWICK_CLOUDFLARE_API_TOKEN added, SHIPWICK_AGENT_DOMAIN changed",
		"✓ The agent is healthy",
		"Next: check that the token can do its job with: shipwick doctor",
		"Next: point an A record for the API's hostname at the server, then sign in to it: shipwick login --url https://<hostname>",
	})
	if strings.Contains(out, "abc_DEF-123") {
		t.Errorf("the token was printed:\n%s", out)
	}
}

func TestServerConfigureRefusesWhatTheInstallerRefuses(t *testing.T) {
	f := newFakeAgent(t)
	remote := &fakeRemote{}
	f.local = localOptions{execInput: remote.exec}
	cases := map[string]struct {
		args []string
		want string
	}{
		"a token with quotes":   {[]string{"SHIPWICK_CLOUDFLARE_API_TOKEN='abc'"}, "SHIPWICK_CLOUDFLARE_API_TOKEN is not a Cloudflare API token: letters, digits, - and _ only, without quotes."},
		"a token with a space":  {[]string{"SHIPWICK_CLOUDFLARE_API_TOKEN=abc def"}, "is not a Cloudflare API token"},
		"a hostname with https": {[]string{"SHIPWICK_AGENT_DOMAIN=https://agent.example.com"}, `SHIPWICK_AGENT_DOMAIN: "https://agent.example.com" is not a hostname. Give a bare name such as agent.example.com — no https://, port or path.`},
		"the same hostnames":    {[]string{"SHIPWICK_AGENT_DOMAIN=x.example.com", "SHIPWICK_DASHBOARD_DOMAIN=x.example.com"}, "The API and the dashboard need two different hostnames."},
		"a line break":          {[]string{"SHIPWICK_WEBHOOK_URL=https://a\nSHIPWICK_AGENT_TOKEN=x"}, "holds a line break"},
		"a setting that is not": {[]string{"SHIPWICK_WEBHOK_URL=https://a"}, "SHIPWICK_WEBHOK_URL is not a setting of the agent"},
		"a lowercase name":      {[]string{"shipwick_webhook_url=https://a"}, "is not a setting of the agent"},
		"the root token":        {[]string{"SHIPWICK_AGENT_TOKEN=abc"}, "SHIPWICK_AGENT_TOKEN is the root token, not a setting"},
		"the encryption key":    {[]string{"SHIPWICK_ENCRYPTION_KEY=abc"}, "shipwick server rotate-key"},
		"not an assignment":     {[]string{"SHIPWICK_WEBHOOK_URL"}, `"SHIPWICK_WEBHOOK_URL" is not KEY=value`},
		"twice":                 {[]string{"SHIPWICK_WEBHOOK_URL=a", "--unset", "SHIPWICK_WEBHOOK_URL"}, "SHIPWICK_WEBHOOK_URL is given twice"},
		"nothing":               {nil, "nothing to change"},
		"unset of an unknown":   {[]string{"--unset", "SHIPWICK_NOPE"}, "SHIPWICK_NOPE is not a setting of the agent"},
	}
	for name, tc := range cases {
		_, _, err := f.run(t.TempDir(), append([]string{"server", "configure", "root@203.0.113.10"}, tc.args...)...)
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, want %q", name, err, tc.want)
		}
	}
	if len(remote.argvs) != 0 {
		t.Errorf("nothing may run before the arguments are checked, ran %q", remote.argvs)
	}
}

func TestServerConfigureExplainsWhatTheScriptCouldNotDo(t *testing.T) {
	f := newFakeAgent(t)
	cases := []struct {
		status int
		stderr string
		want   []string
	}{
		{10, "", []string{"203.0.113.10 has no /opt/shipwick/.env: Shipwick is not installed there", "shipwick server install root@203.0.113.10"}},
		{11, "", []string{"did not become healthy within a minute of the change", "docker compose logs agent", "run this again with the previous values"}},
		{12, "no configuration file provided", []string{"docker compose up failed on root@203.0.113.10; its output is above", "docker compose up -d"}},
		{255, "root@203.0.113.10: Permission denied (publickey).", []string{"203.0.113.10 does not know the SSH key this machine offered"}},
		{255, "ssh: connect to host 203.0.113.10 port 22: Connection timed out", []string{"no answer from 203.0.113.10 on port 22"}},
	}
	for _, tc := range cases {
		remote := &fakeRemote{stderr: tc.stderr, err: fmt.Errorf("exit status %d", tc.status)}
		programs := &fakePrograms{respond: func(_ []string, _, stderr io.Writer) error {
			fmt.Fprintln(stderr, tc.stderr)
			return errors.New("exit status 255")
		}}
		f.local = localOptions{execInput: remote.exec, exec: programs.exec}
		_, _, err := f.run(t.TempDir(), "server", "configure", "root@203.0.113.10", "SHIPWICK_WEBHOOK_URL=https://hooks.example.com/")
		if err == nil {
			t.Fatalf("exit %d: not an error", tc.status)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("exit %d: missing %q in:\n%s", tc.status, want, err)
			}
		}
	}
}

// The list of settings is the installer's: write_env in scripts/install.sh
// carries them from its environment into a new .env, and this command into
// an existing one. A variable added to one and not the other is a setting
// that one of them would refuse.
func TestInstallerVariablesAreTheScripts(t *testing.T) {
	script, err := os.ReadFile(filepath.Join("..", "..", "..", "scripts", "install.sh"))
	if err != nil {
		t.Fatal(err)
	}
	body := string(script)
	start := strings.Index(body, "\nwrite_env() {")
	if start < 0 {
		t.Fatal("write_env not found in scripts/install.sh")
	}
	end := strings.Index(body[start:], "\n}\n")
	writeEnv := body[start : start+end]
	m := regexp.MustCompile(`for name in ((?:[A-Z][A-Z0-9_]* ?)+); do`).FindAllStringSubmatch(writeEnv, -1)
	// The function has two loops: the hostnames it always writes, and the
	// list of settings. The longer one is the list.
	var shell []string
	for _, loop := range m {
		if names := strings.Fields(loop[1]); len(names) > len(shell) {
			shell = names
		}
	}
	if len(shell) < 10 {
		t.Fatalf("the list in write_env was not found; saw %q", m)
	}
	if !reflect.DeepEqual(shell, installerVariables) {
		t.Errorf("installerVariables differ from write_env in scripts/install.sh:\n  script: %q\n  Go:     %q", shell, installerVariables)
	}
}

// The script's shape is checked here, since nothing else reads it: the
// statuses the CLI explains are the ones it exits with, the data comes from
// standard input, and the token line is never a word of it.
func TestRemoteConfigureScriptHasTheShapeTheCLIReads(t *testing.T) {
	for _, want := range []string{"exit 10", "exit 11", "exit 12", `cat > "$changes"`, "umask 077", "chmod 0600 .env", "docker compose up -d", "shipwick-agent healthcheck", `echo restarting`, `echo healthy`} {
		if !strings.Contains(remoteConfigure, want) {
			t.Errorf("the script lacks %q", want)
		}
	}
	if strings.Contains(remoteConfigure, "SHIPWICK_AGENT_TOKEN") {
		t.Error("the script has no business naming the token")
	}
}
