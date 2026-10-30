package docker

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestSocketKindIsReadOffTheDaemonAddress(t *testing.T) {
	for host, want := range map[string]string{
		"unix:///run/docker-proxy/docker.sock": SocketProxy, // the compose files
		"tcp://socket-proxy:2375":              SocketProxy, // the service's name, on a port
		"http://socket-proxy:2375":             SocketProxy,
		"unix:///var/run/docker.sock":          SocketDirect, // the default, and the package
		"unix:///run/user/1000/docker.sock":    SocketDirect, // rootless, held directly
		"unix:///run/docker-proxy.sock":        SocketDirect, // not the proxy's directory
		"tcp://127.0.0.1:2375":                 SocketDirect,
		"npipe:////./pipe/docker_engine":       SocketDirect,
		"":                                     SocketDirect,
		"://not an address":                    SocketDirect,
	} {
		if got := socketKind(host); got != want {
			t.Errorf("socketKind(%q) = %q, want %q", host, got, want)
		}
	}
}

// The allow-list of the socket proxy is the calls this package makes, and it
// is written twice: in configs/compose.production.yml, which the installer
// puts on servers, and in docker-compose.yml, so that development runs what
// production runs. A call added to one and not the other would pass here and
// be refused there, or the other way round.
func TestTheDevelopmentStackRunsTheProductionSocketProxy(t *testing.T) {
	root := filepath.Join("..", "..", "..")
	production := socketProxyService(t, filepath.Join(root, "configs", "compose.production.yml"))
	development := socketProxyService(t, filepath.Join(root, "docker-compose.yml"))
	if production != development {
		t.Errorf("the socket-proxy service of docker-compose.yml differs from the one of configs/compose.production.yml; copy the production service over\n--- production\n%s\n--- development\n%s", production, development)
	}
	for _, file := range []string{filepath.Join("configs", "compose.production.yml"), "docker-compose.yml"} {
		body, err := os.ReadFile(filepath.Join(root, file))
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(string(body), "DOCKER_HOST: unix:///run/docker-proxy/docker.sock") {
			t.Errorf("%s does not point the agent at the proxy's socket", file)
		}
		if strings.Contains(string(body), "- /var/run/docker.sock:/var/run/docker.sock") {
			t.Errorf("%s mounts the Docker socket into a service other than the proxy, which mounts it through SHIPWICK_DOCKER_SOCKET", file)
		}
	}
}

// socketProxyService is the body of the socket-proxy service in a compose
// file: the lines indented under it, comments included, up to the first line
// that is not.
func socketProxyService(t *testing.T, path string) string {
	t.Helper()
	body, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	lines := strings.Split(string(body), "\n")
	start := -1
	for i, line := range lines {
		if line == "  socket-proxy:" {
			start = i + 1
			break
		}
	}
	if start < 0 {
		t.Fatalf("%s has no socket-proxy service", path)
	}
	end := start
	for end < len(lines) && strings.HasPrefix(lines[end], "    ") {
		end++
	}
	if end == start {
		t.Fatalf("%s: the socket-proxy service is empty", path)
	}
	return strings.Join(lines[start:end], "\n")
}
