package commands

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"regexp"
	"strings"
)

// explainSSHFailure turns ssh's last line into what it means and what to do,
// for a connection that failed before any command ran. For a refused login
// it runs ssh once more with -v, the only way to learn which key ssh
// offered; nothing of what -v prints is shown but the key's name and
// fingerprint, which are the public half.
func (c *cli) explainSSHFailure(ctx context.Context, local localOptions, t sshTarget, err error, stderr string) error {
	var offered []offeredKey
	if isPermissionDenied(stderr) {
		var verbose bytes.Buffer
		argv := append([]string{"ssh", "-v"}, sshArgv(t, remoteUname)[1:]...)
		_ = local.exec(ctx, argv, io.Discard, &verbose)
		offered = offeredKeys(verbose.String())
	}
	return describeSSHError(err, t, stderr, offered)
}

func isPermissionDenied(stderr string) bool {
	return strings.Contains(stderr, "Permission denied")
}

// describeSSHError is explainSSHFailure's sentence. The cases are ssh's own
// last lines; each means one thing, and one thing to do about it.
func describeSSHError(err error, t sshTarget, stderr string, offered []offeredKey) error {
	if errors.Is(err, exec.ErrNotFound) {
		return errors.New("ssh was not found on this machine\n\nInstall the OpenSSH client (Debian/Ubuntu: apt install openssh-client; Windows: Settings → System → Optional features → OpenSSH Client) and run this again")
	}
	detail := strings.TrimSpace(stderr)
	if detail == "" {
		detail = err.Error()
	}
	switch {
	// A key that changed is what ssh refuses on purpose: a reinstalled
	// server, or someone in the middle. Only the person who reinstalled it
	// can tell which.
	case strings.Contains(detail, "REMOTE HOST IDENTIFICATION HAS CHANGED"), strings.Contains(detail, "Host key verification failed"):
		return fmt.Errorf("the SSH host key of %s is not the one this machine knows:\n  %s\n\nA server that was rebuilt — deleted and created again at the provider, or reinstalled — has a new key. If that is what happened, forget the old one with:\n  ssh-keygen -R %s\nand run this again. If it is not, do not connect until you know why the key changed", t, firstLine(detail), t.host)
	case isPermissionDenied(detail):
		return keyNotKnown(t, detail, offered)
	case strings.Contains(detail, "Connection refused"):
		return fmt.Errorf("nothing accepts SSH connections on %s port %s:\n  %s\n\nThe server is still starting, or its SSH listens on another port: name it in ~/.ssh/config —\n  Host %s\n    Port 2222\n— then run this again", t.host, sshPort(detail), firstLine(detail), t.host)
	case strings.Contains(detail, "timed out"), strings.Contains(detail, "Connection timeout"), strings.Contains(detail, "No route to host"), strings.Contains(detail, "Network is unreachable"):
		return fmt.Errorf("no answer from %s on port %s within 15 seconds:\n  %s\n\nThe address is wrong, SSH listens on another port (name it in ~/.ssh/config: Host %s, Port 2222), or a firewall in front of the server admits SSH from certain addresses only and this machine's is not among them — the provider's firewall is where to look. Then run this again", t.host, sshPort(detail), firstLine(detail), t.host)
	case strings.Contains(detail, "Could not resolve hostname"), strings.Contains(detail, "Name or service not known"), strings.Contains(detail, "nodename nor servname"):
		return fmt.Errorf("%s is not a name this machine can resolve:\n  %s\n\nCheck the spelling, or give the server's IP address instead; then run this again", t.host, firstLine(detail))
	}
	return fmt.Errorf("could not log in to %s over SSH:\n  %s\n\nThe login must work without a password: add your key with ssh-copy-id %s, or check the host in ~/.ssh/config, then run this again", t, detail, t)
}

// keyNotKnown is the refusal that stopped a real installation on its first
// command: the server has an authorized_keys without the key ssh offered.
// The ways out are at the provider, because without a key nothing of ours
// gets in to put one there.
func keyNotKnown(t sshTarget, detail string, offered []offeredKey) error {
	var b strings.Builder
	fmt.Fprintf(&b, "%s does not know the SSH key this machine offered:\n  %s\n", t.host, firstLine(detail))
	switch len(offered) {
	case 0:
		b.WriteString("\nssh offered no key at all: none of the usual files (~/.ssh/id_ed25519, ~/.ssh/id_rsa) exists and no agent holds one. Create one with: ssh-keygen -t ed25519\n")
	default:
		b.WriteString("\nOffered, and refused:\n")
		for _, k := range offered {
			fmt.Fprintf(&b, "  %s\n", k)
		}
	}
	fmt.Fprintf(&b, "\nThe public key has to be in %s on the server, and shipwick never types a password. Three ways to put it there:\n", authorizedKeys(t.user))
	b.WriteString("  - when the server is created at the provider, select this key: it is in place from the first boot;\n")
	b.WriteString("  - on a server that exists, through the provider's console or rescue system, paste the public key into that file;\n")
	b.WriteString("  - or with the provider's own way of installing a key on a running server, where it has one.\n")
	if strings.Contains(firstLine(detail), "password") {
		b.WriteString("\nThe server would take a password too (it says so in the parentheses); with it, ssh-copy-id " + t.String() + " puts the key there once, and this command needs the password never again.")
	} else {
		b.WriteString("\nThen run this again.")
	}
	return errors.New(b.String())
}

// portInLine is the port in "ssh: connect to host 203.0.113.10 port 22".
var portInLine = regexp.MustCompile(` port (\d+)`)

// sshPort is the port ssh tried, which ~/.ssh/config may have changed from
// the default; 22 when ssh did not say.
func sshPort(detail string) string {
	if m := portInLine.FindStringSubmatch(firstLine(detail)); m != nil {
		return m[1]
	}
	return "22"
}

// authorizedKeys is where sshd looks for user's keys.
func authorizedKeys(user string) string {
	if user == "root" {
		return "/root/.ssh/authorized_keys"
	}
	return "~" + user + "/.ssh/authorized_keys"
}

// offeredKey is one key ssh -v said it offered: the file it came from, or
// the agent's name for it, with the type and the fingerprint of its public
// half.
type offeredKey struct {
	name, keyType, fingerprint string
	agent                      bool
}

func (k offeredKey) String() string {
	where := k.name
	switch {
	case k.agent:
		where += " (in the ssh agent)"
	case strings.HasPrefix(k.name, "/"), strings.HasPrefix(k.name, "~"), strings.Contains(k.name, ":\\"), strings.Contains(k.name, ":/"):
		where += ".pub"
	}
	return strings.TrimSpace(where + "  " + k.keyType + " " + k.fingerprint)
}

// offeringLine is OpenSSH's debug line for a key it sent: the identity's
// name, its type, its fingerprint, and "agent" or "explicit" where the key
// did not come from a file of the default list.
var offeringLine = regexp.MustCompile(`(?m)^debug1: Offering public key: (.+?) (\S+) (SHA256:\S+)( agent| explicit)?\s*$`)

// offeredKeys reads ssh -v's output for the keys it offered, in the order it
// offered them, each once.
func offeredKeys(verbose string) []offeredKey {
	var keys []offeredKey
	seen := map[string]bool{}
	for _, m := range offeringLine.FindAllStringSubmatch(verbose, -1) {
		if seen[m[3]] {
			continue
		}
		seen[m[3]] = true
		keys = append(keys, offeredKey{name: m[1], keyType: m[2], fingerprint: m[3], agent: strings.TrimSpace(m[4]) == "agent"})
	}
	return keys
}

// firstLine is the line of ssh's output that says what happened, without
// the banner of asterisks it prints around a changed key.
func firstLine(s string) string {
	for _, line := range strings.Split(s, "\n") {
		line = strings.TrimSpace(line)
		if line != "" && !strings.HasPrefix(line, "@") {
			return line
		}
	}
	return s
}
