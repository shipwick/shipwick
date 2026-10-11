package commands

import (
	"errors"
	"fmt"
	"io"
	"reflect"
	"strings"
	"testing"
)

// sshVerbose is what `ssh -v` says on the way to a refusal, with two keys
// offered — one from a file, one from the agent — and one not found.
const sshVerbose = `OpenSSH_9.6p1 Ubuntu-3ubuntu13, OpenSSL 3.0.13 30 Jan 2024
debug1: Reading configuration data /home/ada/.ssh/config
debug1: Will attempt key: /home/ada/.ssh/id_ed25519 ED25519 SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
debug1: Will attempt key: ada@laptop RSA SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb agent
debug1: Authentications that can continue: publickey,password
debug1: Next authentication method: publickey
debug1: Offering public key: /home/ada/.ssh/id_ed25519 ED25519 SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
debug1: Authentications that can continue: publickey,password
debug1: Offering public key: ada@laptop RSA SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb agent
debug1: Authentications that can continue: publickey,password
debug1: Trying private key: /home/ada/.ssh/id_rsa
debug1: No more authentication methods to try.
root@203.0.113.10: Permission denied (publickey,password).
`

// sshFailingWith stands in for an ssh whose first run fails with lastLine
// and whose -v run prints verbose.
func sshFailingWith(lastLine, verbose string) *fakePrograms {
	return &fakePrograms{respond: func(argv []string, _, stderr io.Writer) error {
		if argv[1] == "-v" {
			fmt.Fprint(stderr, verbose)
		} else {
			fmt.Fprintln(stderr, lastLine)
		}
		return errors.New("exit status 255")
	}}
}

func TestARefusedKeyIsNamedWithItsFingerprintAndTheWaysOut(t *testing.T) {
	f := newFakeAgent(t)
	programs := sshFailingWith("root@203.0.113.10: Permission denied (publickey,password).", sshVerbose)
	f.local = localOptions{exec: programs.exec}
	_, _, err := f.run(t.TempDir(), "server", "install", "root@203.0.113.10")
	if err == nil {
		t.Fatal("a refused login is an error")
	}
	got := err.Error()
	for _, want := range []string{
		"203.0.113.10 does not know the SSH key this machine offered",
		"root@203.0.113.10: Permission denied (publickey,password).",
		"/home/ada/.ssh/id_ed25519.pub  ED25519 SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
		"ada@laptop (in the ssh agent)  RSA SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		"/root/.ssh/authorized_keys",
		"when the server is created at the provider, select this key",
		"console or rescue system",
		"provider's own way of installing a key",
		"would take a password too",
		"ssh-copy-id root@203.0.113.10",
	} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "id_rsa") || strings.Contains(got, "debug1") {
		t.Errorf("a key that was not offered, or ssh's chatter, has no place here:\n%s", got)
	}

	// ssh ran twice: once as always, once with -v for the keys, and the -v
	// run is the same command with one flag more.
	if len(programs.argvs) != 2 || programs.argvs[1][1] != "-v" || !reflect.DeepEqual(programs.argvs[1][2:], programs.argvs[0][1:]) {
		t.Errorf("ssh was run as %q", programs.argvs)
	}
}

func TestARefusedKeyWithoutAPasswordSaysSoWithoutSshCopyId(t *testing.T) {
	f := newFakeAgent(t)
	// A key-only server, and no key to offer: the usual state of a laptop
	// that never made one.
	programs := sshFailingWith("deploy@box.example.net: Permission denied (publickey).", "debug1: No more authentication methods to try.\ndeploy@box.example.net: Permission denied (publickey).\n")
	f.local = localOptions{exec: programs.exec}
	_, _, err := f.run(t.TempDir(), "server", "install", "deploy@box.example.net")
	if err == nil {
		t.Fatal("a refused login is an error")
	}
	got := err.Error()
	for _, want := range []string{"ssh offered no key at all", "ssh-keygen -t ed25519", "~deploy/.ssh/authorized_keys", "Then run this again."} {
		if !strings.Contains(got, want) {
			t.Errorf("missing %q in:\n%s", want, got)
		}
	}
	if strings.Contains(got, "ssh-copy-id") || strings.Contains(got, "password too") {
		t.Errorf("without a password there is nothing for ssh-copy-id to use:\n%s", got)
	}
}

func TestConnectionFailuresAreToldApart(t *testing.T) {
	f := newFakeAgent(t)
	cases := []struct {
		lastLine string
		want     []string
		not      string
	}{
		{"ssh: connect to host 203.0.113.10 port 22: Connection refused",
			[]string{"nothing accepts SSH connections on 203.0.113.10 port 22", "still starting", "Host 203.0.113.10", "Port 2222"}, "firewall"},
		// ~/.ssh/config may have moved the port already; the line says which.
		{"ssh: connect to host 203.0.113.10 port 2298: Connection refused",
			[]string{"nothing accepts SSH connections on 203.0.113.10 port 2298"}, "port 22:"},
		{"ssh: connect to host 203.0.113.10 port 22: Connection timed out",
			[]string{"no answer from 203.0.113.10 on port 22 within 15 seconds", "address is wrong", "another port", "firewall in front of the server admits SSH from certain addresses only"}, "ssh-copy-id"},
		{"ssh: connect to host 203.0.113.10 port 22: Operation timed out",
			[]string{"no answer from 203.0.113.10"}, "ssh-copy-id"},
		{"ssh: Could not resolve hostname 203.0.113.10: Name or service not known",
			[]string{"not a name this machine can resolve", "IP address instead"}, "firewall"},
		{"@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\n@    WARNING: REMOTE HOST IDENTIFICATION HAS CHANGED!     @\n@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@@\nHost key verification failed.",
			[]string{"host key of root@203.0.113.10 is not the one this machine knows", "A server that was rebuilt", "ssh-keygen -R 203.0.113.10"}, "@@@"},
	}
	for _, tc := range cases {
		programs := sshFailingWith(tc.lastLine, "")
		f.local = localOptions{exec: programs.exec}
		_, _, err := f.run(t.TempDir(), "server", "install", "root@203.0.113.10")
		if err == nil {
			t.Fatalf("%q: not an error", tc.lastLine)
		}
		for _, want := range tc.want {
			if !strings.Contains(err.Error(), want) {
				t.Errorf("%q: missing %q in:\n%s", tc.lastLine, want, err)
			}
		}
		if strings.Contains(err.Error(), tc.not) {
			t.Errorf("%q: %q does not belong in:\n%s", tc.lastLine, tc.not, err)
		}
		// Only a refused key is worth a second run with -v.
		if len(programs.argvs) != 1 {
			t.Errorf("%q: ssh was run %d times", tc.lastLine, len(programs.argvs))
		}
	}
}

func TestOfferedKeysReadsEachOnce(t *testing.T) {
	keys := offeredKeys(sshVerbose + sshVerbose)
	want := []offeredKey{
		{name: "/home/ada/.ssh/id_ed25519", keyType: "ED25519", fingerprint: "SHA256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"},
		{name: "ada@laptop", keyType: "RSA", fingerprint: "SHA256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", agent: true},
	}
	if !reflect.DeepEqual(keys, want) {
		t.Errorf("offeredKeys = %+v", keys)
	}
	// A Windows path is a file too, and a key given with -i is neither.
	if got := (offeredKey{name: `C:\Users\ada\.ssh\id_ed25519`, keyType: "ED25519", fingerprint: "SHA256:x"}).String(); got != `C:\Users\ada\.ssh\id_ed25519.pub  ED25519 SHA256:x` {
		t.Errorf("String() = %q", got)
	}
	if got := offeredKeys("debug1: Offering public key: deploy-key ED25519 SHA256:y explicit\n"); len(got) != 1 || got[0].agent || got[0].String() != "deploy-key  ED25519 SHA256:y" {
		t.Errorf("an explicit key: %+v", got)
	}
}

// The -v run never prints what ssh says: a private key's path is in there,
// and the chatter would bury the sentence.
func TestTheVerboseRunIsNotShown(t *testing.T) {
	f := newFakeAgent(t)
	programs := sshFailingWith("root@203.0.113.10: Permission denied (publickey).", sshVerbose)
	f.local = localOptions{exec: programs.exec}
	out, errOut, _ := f.run(t.TempDir(), "server", "install", "root@203.0.113.10")
	if strings.Contains(out+errOut, "debug1") {
		t.Errorf("ssh -v was echoed:\n%s%s", out, errOut)
	}
}
