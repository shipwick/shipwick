package docker

import (
	"net/url"
	"strings"
)

// How the agent reaches the daemon, as Info.Socket and GET /server name it.
const (
	// SocketProxy: through the filtered socket of the installation's socket
	// proxy, which answers the calls on its list and refuses every other.
	SocketProxy = "proxy"
	// SocketDirect: the daemon's own socket, or whatever else DOCKER_HOST
	// names. Whoever holds it has the whole Engine API.
	SocketDirect = "direct"
)

// socketKind reads off the daemon address the client was given whether it is
// the socket proxy's: a unix socket under /run/docker-proxy/, where the
// compose files have the proxy make it, or a TCP address at a host named
// socket-proxy, the service's name. Anything else — /var/run/docker.sock
// above all — is the daemon itself. The agent is not told which arrangement
// it runs in; the address is the one fact it has, and the compose files are
// what make it true.
func socketKind(host string) string {
	u, err := url.Parse(host)
	if err != nil {
		return SocketDirect
	}
	switch u.Scheme {
	case "unix":
		if strings.HasPrefix(u.Path, "/run/docker-proxy/") {
			return SocketProxy
		}
	case "tcp", "http", "https":
		if u.Hostname() == "socket-proxy" {
			return SocketProxy
		}
	}
	return SocketDirect
}
