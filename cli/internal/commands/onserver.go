package commands

import (
	"errors"
	"io/fs"
	"net"
	"net/url"
	"os"
	"path"
)

// EnvInstallDir is the installer's variable for where the server lives.
const EnvInstallDir = "SHIPWICK_INSTALL_DIR"

const defaultInstallDir = "/opt/shipwick"

// machine is what Render asks about the computer it runs on. Render takes
// only the error, so these are the process's own; tests replace them.
var machine = struct {
	getenv func(string) string
	stat   func(string) (fs.FileInfo, error)
}{os.Getenv, os.Stat}

// installedServer returns the .env of a Shipwick server installed on this
// machine, or "" when there is none. The file is root's: a failure to look
// at it that is not "no such file" still says the server is here.
func installedServer() string {
	dir := machine.getenv(EnvInstallDir)
	if dir == "" {
		dir = defaultInstallDir
	}
	env := path.Join(dir, ".env")
	if _, err := machine.stat(env); errors.Is(err, fs.ErrNotExist) {
		return ""
	}
	return env
}

// onServerHint is what to say when no agent answers at agentURL and the
// command runs on the server itself: the tunnel a laptop would open leads
// nowhere there, because the agent's port is not published. It is "" for
// any other situation, an address that is not this machine's included.
func onServerHint(agentURL string) string {
	u, err := url.Parse(agentURL)
	if err != nil {
		return ""
	}
	if host := u.Hostname(); host != "localhost" {
		if ip := net.ParseIP(host); ip == nil || !ip.IsLoopback() {
			return ""
		}
	}
	env := installedServer()
	if env == "" {
		return ""
	}
	return "The agent on this server publishes no port. Give it a hostname (SHIPWICK_AGENT_DOMAIN in " + env +
		", then run the installer again), or see \"Reach the API without a hostname\" in the handbook, Installation."
}
