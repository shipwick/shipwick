package dockertest

import (
	"context"
	"fmt"
	"sort"
	"strings"
)

// ProxyID is the id of the fake's reverse proxy container. It is not a
// Shipwick-managed container and never appears in ListContainers; its files
// live in the fake's filesystem like any container's, so tests can read what
// a static deployment put there with Files(ProxyID).
const ProxyID = "proxy"

// ProxyContainer is the fake's stand-in for the reverse proxy's container.
// ProxyErr, when set, is what it answers instead: a proxy outside Docker.
func (f *Fake) ProxyContainer(ctx context.Context) (string, error) {
	if f.ProxyErr != nil {
		return "", f.ProxyErr
	}
	return ProxyID, nil
}

// execProxy runs the few commands the engine uses inside the proxy — the
// ones Caddy's image has — against the fake's filesystem. A result set for
// the command in ExecResults wins, so a test can make one of them fail.
// Called with f.mu held.
func (f *Fake) execProxy(cmd []string) (int, string, error) {
	f.execCalls = append(f.execCalls, ExecCall{Container: ProxyID, Cmd: append([]string(nil), cmd...)})
	if r, ok := f.ExecResults[strings.Join(cmd, " ")]; ok {
		return r.ExitCode, r.Output, r.Err
	}
	files := f.files[ProxyID]
	if files == nil {
		files = map[string][]byte{}
		f.files[ProxyID] = files
	}
	rel := func(p string) string { return strings.TrimPrefix(p, "/") }
	switch {
	case len(cmd) == 3 && cmd[0] == "mkdir" && cmd[1] == "-p":
		// Directories are implied by the files in them; an empty one is kept
		// as a marker so that `ls` and `test -d` see it.
		f.proxyDirs[rel(cmd[2])] = true
		return 0, "", nil
	case len(cmd) == 3 && cmd[0] == "test" && cmd[1] == "-f":
		if _, ok := files[rel(cmd[2])]; ok {
			return 0, "", nil
		}
		return 1, "", nil
	case len(cmd) == 3 && cmd[0] == "test" && cmd[1] == "-d":
		if f.proxyDirExists(rel(cmd[2])) {
			return 0, "", nil
		}
		return 1, "", nil
	case len(cmd) == 3 && cmd[0] == "rm" && cmd[1] == "-rf":
		dir := rel(cmd[2])
		for name := range files {
			if name == dir || strings.HasPrefix(name, dir+"/") {
				delete(files, name)
			}
		}
		for name := range f.proxyDirs {
			if name == dir || strings.HasPrefix(name, dir+"/") {
				delete(f.proxyDirs, name)
			}
		}
		f.proxyRemoved = append(f.proxyRemoved, cmd[2])
		return 0, "", nil
	case len(cmd) == 3 && cmd[0] == "mv":
		from, to := rel(cmd[1]), rel(cmd[2])
		if !f.proxyDirExists(from) {
			return 1, "mv: can't rename '" + cmd[1] + "': No such file or directory", nil
		}
		for name, content := range files {
			if strings.HasPrefix(name, from+"/") {
				files[to+strings.TrimPrefix(name, from)] = content
				delete(files, name)
			}
		}
		for name := range f.proxyDirs {
			if name == from || strings.HasPrefix(name, from+"/") {
				f.proxyDirs[to+strings.TrimPrefix(name, from)] = true
				delete(f.proxyDirs, name)
			}
		}
		return 0, "", nil
	case len(cmd) == 3 && cmd[0] == "ls" && cmd[1] == "-1":
		dir := rel(cmd[2])
		if !f.proxyDirExists(dir) {
			return 1, "ls: " + cmd[2] + ": No such file or directory", nil
		}
		return 0, strings.Join(f.proxyEntries(dir), "\n"), nil
	}
	return 0, "", fmt.Errorf("the fake proxy cannot run %q", strings.Join(cmd, " "))
}

// proxyDirExists reports whether dir was made or holds a file. Called with
// f.mu held.
func (f *Fake) proxyDirExists(dir string) bool {
	if f.proxyDirs[dir] {
		return true
	}
	for name := range f.files[ProxyID] {
		if strings.HasPrefix(name, dir+"/") {
			return true
		}
	}
	return false
}

// proxyEntries lists the direct children of dir, sorted. Called with f.mu held.
func (f *Fake) proxyEntries(dir string) []string {
	seen := map[string]bool{}
	note := func(name string) {
		if child, ok := strings.CutPrefix(name, dir+"/"); ok && child != "" {
			seen[strings.SplitN(child, "/", 2)[0]] = true
		}
	}
	for name := range f.files[ProxyID] {
		note(name)
	}
	for name := range f.proxyDirs {
		note(name)
	}
	out := make([]string, 0, len(seen))
	for name := range seen {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

// ProxyRemoved lists the paths removed from the proxy so far, in order.
func (f *Fake) ProxyRemoved() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.proxyRemoved...)
}
