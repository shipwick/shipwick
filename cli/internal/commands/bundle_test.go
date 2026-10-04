package commands

import (
	"archive/tar"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

// fakeRelease serves the files of one release, with a checksums.txt that
// holds their real hashes unless a test says otherwise.
type fakeRelease struct {
	tag    string
	assets map[string]string
	// wrong names an asset whose published checksum is not its own.
	wrong string
}

func newFakeRelease() *fakeRelease {
	return &fakeRelease{tag: "v0.6.0", assets: map[string]string{
		"compose.production.yml": "services:\n  agent:\n    image: ${SHIPWICK_AGENT_IMAGE:-ghcr.io/shipwick/agent:0.6.0}\n" +
			"  caddy:\n    image: ${SHIPWICK_CADDY_IMAGE:-ghcr.io/shipwick/caddy:0.6.0}\n" +
			"  dashboard:\n    image: ${SHIPWICK_DASHBOARD_IMAGE:-ghcr.io/shipwick/dashboard:0.6.0}\n",
		"install.sh":           "#!/bin/sh\necho installer\n",
		"shipwick_linux_amd64": "binary for amd64\n",
		"shipwick_linux_arm64": "binary for arm64\n",
	}}
}

func (r *fakeRelease) serve(t *testing.T) upgradeOptions {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/shipwick/shipwick/releases/latest", func(w http.ResponseWriter, _ *http.Request) {
		fmt.Fprintf(w, `{"tag_name":%q,"prerelease":false}`, r.tag)
	})
	mux.HandleFunc("GET /shipwick/shipwick/releases/download/{tag}/{asset}", func(w http.ResponseWriter, req *http.Request) {
		if req.PathValue("tag") != r.tag {
			http.NotFound(w, req)
			return
		}
		if req.PathValue("asset") == "checksums.txt" {
			for name, content := range r.assets {
				if name == r.wrong {
					content += "tampered"
				}
				sum := sha256.Sum256([]byte(content))
				fmt.Fprintf(w, "%s  %s\n", hex.EncodeToString(sum[:]), name)
			}
			return
		}
		content, ok := r.assets[req.PathValue("asset")]
		if !ok {
			http.NotFound(w, req)
			return
		}
		fmt.Fprint(w, content)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return upgradeOptions{api: srv.URL, web: srv.URL}
}

// bundleDocker stands in for `docker pull` and `docker save`: it records what
// it was asked and writes an archive where save was told to.
type bundleDocker struct {
	calls [][]string
	// oldSave makes it a Docker from before `save --platform`.
	oldSave bool
	fail    string // "pull" or "save": that command fails
	// saved is the archive save writes; nil: a line that names the images.
	saved []byte
}

func (d *bundleDocker) run(_ context.Context, _ string, argv []string, out io.Writer) error {
	d.calls = append(d.calls, argv)
	if argv[1] == d.fail {
		fmt.Fprintln(out, "Error response from daemon: Get \"https://ghcr.io/v2/\": dial tcp: i/o timeout")
		return errors.New("exit status 1")
	}
	if argv[1] != "save" {
		return nil
	}
	for i, arg := range argv {
		if arg == "--platform" && d.oldSave {
			fmt.Fprintln(out, "unknown flag: --platform")
			return errors.New("exit status 125")
		}
		if arg == "--output" {
			if d.saved != nil {
				return os.WriteFile(argv[i+1], d.saved, 0o644)
			}
			return os.WriteFile(argv[i+1], []byte("images: "+strings.Join(argv[i+2:], " ")), 0o644)
		}
	}
	return errors.New("save without --output")
}

func (d *bundleDocker) ran(command string) (n int) {
	for _, argv := range d.calls {
		if argv[1] == command {
			n++
		}
	}
	return n
}

// readBundle returns the files of a bundle by name, with their modes.
func readBundle(t *testing.T, path string) (files map[string]string, modes map[string]int64) {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	zr, err := gzip.NewReader(f)
	if err != nil {
		t.Fatal(err)
	}
	files, modes = map[string]string{}, map[string]int64{}
	for tr := tar.NewReader(zr); ; {
		h, err := tr.Next()
		if err == io.EOF {
			return files, modes
		}
		if err != nil {
			t.Fatal(err)
		}
		data, _ := io.ReadAll(tr)
		files[h.Name], modes[h.Name] = string(data), h.Mode
	}
}

func TestServerBundleHoldsTheReleaseAndItsImagesForTheServersArchitecture(t *testing.T) {
	release, docker := newFakeRelease(), &bundleDocker{}
	f := newFakeAgent(t)
	f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}
	dir := t.TempDir()

	out, _, err := f.run(dir, "server", "bundle", "--arch", "arm64")
	if err != nil {
		t.Fatalf("server bundle: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Release v0.6.0", "match its checksums", "Wrote shipwick-v0.6.0-linux-arm64.tar.gz",
		"tar -xzf shipwick-v0.6.0-linux-arm64.tar.gz", "sh shipwick-v0.6.0-linux-arm64/install.sh"})

	files, modes := readBundle(t, filepath.Join(dir, "shipwick-v0.6.0-linux-arm64.tar.gz"))
	top := "shipwick-v0.6.0-linux-arm64/"
	for name, want := range map[string]string{
		"compose.production.yml": release.assets["compose.production.yml"],
		"install.sh":             release.assets["install.sh"],
		"shipwick_linux_arm64":   "binary for arm64\n",
		"images.tar":             "images: ghcr.io/shipwick/agent:0.6.0 ghcr.io/shipwick/caddy:0.6.0 ghcr.io/shipwick/dashboard:0.6.0",
	} {
		if files[top+name] != want {
			t.Errorf("%s = %q, want %q", name, files[top+name], want)
		}
	}
	if _, other := files[top+"shipwick_linux_amd64"]; other {
		t.Error("the bundle holds the binary of another architecture")
	}
	if !strings.Contains(files[top+"checksums.txt"], "  install.sh\n") {
		t.Errorf("checksums.txt is not the release's:\n%s", files[top+"checksums.txt"])
	}
	sum := sha256.Sum256([]byte(files[top+"images.tar"]))
	if want := hex.EncodeToString(sum[:]) + "  images.tar\n"; files[top+"images.tar.sha256"] != want {
		t.Errorf("images.tar.sha256 = %q, want %q", files[top+"images.tar.sha256"], want)
	}
	if modes[top+"install.sh"] != 0o755 || modes[top+"shipwick_linux_arm64"] != 0o755 || modes[top+"images.tar"] != 0o644 {
		t.Errorf("modes = %v", modes)
	}

	if docker.ran("pull") != 3 {
		t.Fatalf("docker calls = %v, want three pulls", docker.calls)
	}
	for _, argv := range docker.calls {
		if !strings.Contains(strings.Join(argv, " "), "--platform linux/arm64") {
			t.Errorf("%v does not ask for the server's platform", argv)
		}
	}
	left, _ := os.ReadDir(dir)
	if len(left) != 1 {
		t.Errorf("the working files were left behind: %v", left)
	}
}

func TestServerBundleRefusesAFileThatDoesNotMatchTheReleasesChecksums(t *testing.T) {
	release, docker := newFakeRelease(), &bundleDocker{}
	release.wrong = "compose.production.yml"
	f := newFakeAgent(t)
	f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}
	dir := t.TempDir()

	_, _, err := f.run(dir, "server", "bundle", "--version", "v0.6.0")
	if err == nil || !strings.Contains(err.Error(), "compose.production.yml does not match the checksum published with the release; no bundle was written") {
		t.Fatalf("err = %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 || len(docker.calls) != 0 {
		t.Errorf("left behind %v, docker ran %v", left, docker.calls)
	}
}

func TestServerBundleSaysWhichReleasesCanBeBundled(t *testing.T) {
	release := newFakeRelease()
	delete(release.assets, "install.sh")
	f := newFakeAgent(t)
	f.upgrade = release.serve(t)

	_, _, err := f.run(t.TempDir(), "server", "bundle")
	if err == nil || !strings.Contains(err.Error(), "release v0.6.0 does not publish its installer: a bundle can be made of 0.6.0 and later") {
		t.Fatalf("err = %v", err)
	}
}

func TestServerBundleSavesWithADockerFromBeforeSavePlatform(t *testing.T) {
	release, docker := newFakeRelease(), &bundleDocker{oldSave: true}
	f := newFakeAgent(t)
	f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}
	dir := t.TempDir()

	if out, _, err := f.run(dir, "server", "bundle", "-o", "bundle.tar.gz"); err != nil {
		t.Fatalf("server bundle: %v\n%s", err, out)
	}
	if docker.ran("save") != 2 {
		t.Errorf("docker calls = %v, want a second save without --platform", docker.calls)
	}
	files, _ := readBundle(t, filepath.Join(dir, "bundle.tar.gz"))
	if !strings.HasPrefix(files["shipwick-v0.6.0-linux-amd64/images.tar"], "images: ") {
		t.Errorf("no images in the bundle: %v", files)
	}
}

func TestServerBundleWithNoPullSavesWhatTheMachineHas(t *testing.T) {
	release, docker := newFakeRelease(), &bundleDocker{}
	f := newFakeAgent(t)
	f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}

	if out, _, err := f.run(t.TempDir(), "server", "bundle", "--no-pull"); err != nil {
		t.Fatalf("server bundle: %v\n%s", err, out)
	}
	if docker.ran("pull") != 0 || docker.ran("save") != 1 {
		t.Errorf("docker calls = %v", docker.calls)
	}
}

func TestServerBundleReportsWhatDockerSaid(t *testing.T) {
	release, docker := newFakeRelease(), &bundleDocker{fail: "pull"}
	f := newFakeAgent(t)
	f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}
	dir := t.TempDir()

	_, _, err := f.run(dir, "server", "bundle")
	if err == nil || !strings.Contains(err.Error(), "could not pull ghcr.io/shipwick/agent:0.6.0: Error response from daemon") {
		t.Fatalf("err = %v", err)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("left behind: %v", left)
	}
}

func TestServerBundleRejectsWhatIsNotAReleaseOrAnArchitecture(t *testing.T) {
	f := newFakeAgent(t)
	f.upgrade = newFakeRelease().serve(t)
	if _, _, err := f.run(t.TempDir(), "server", "bundle", "--arch", "riscv64"); err == nil || !strings.Contains(err.Error(), "amd64 or arm64") {
		t.Errorf("--arch riscv64: %v", err)
	}
	if _, _, err := f.run(t.TempDir(), "server", "bundle", "--version", "latest"); err == nil || !strings.Contains(err.Error(), "releases look like v0.6.0") {
		t.Errorf("--version latest: %v", err)
	}
}

func TestDoctorSaysWhenTheAgentHasAProxyAndTheDockerDaemonHasNone(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "1.2.3", DockerVersion: "29.0.0",
		Network: &api.NetworkStatus{Proxy: "proxy.example.com:3128"}}
	out, _, _ := f.run(t.TempDir(), "doctor")
	if !strings.Contains(out, "The agent goes through the proxy proxy.example.com:3128, and the Docker daemon on the server has none configured") ||
		!strings.Contains(out, "/etc/docker/daemon.json") {
		t.Errorf("doctor does not explain the mismatch:\n%s", out)
	}

	f.server.Network.DockerProxy = true
	out, _, _ = f.run(t.TempDir(), "doctor")
	if !strings.Contains(out, "The agent and the Docker daemon go through a proxy (proxy.example.com:3128)") || strings.Contains(out, "daemon.json") {
		t.Errorf("doctor with a proxy on both sides:\n%s", out)
	}

	// An agent from before 0.6, and one with nothing in the way.
	for _, network := range []*api.NetworkStatus{nil, {DNSResolvers: []string{}}} {
		f.server.Network = network
		if out, _, _ = f.run(t.TempDir(), "doctor"); strings.Contains(out, "proxy (") || strings.Contains(out, "goes through the proxy") {
			t.Errorf("doctor mentions a proxy nobody has:\n%s", out)
		}
	}
}

func TestServerStatusShowsHowTheServerReachesTheInternet(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "1.2.3", Network: &api.NetworkStatus{
		Proxy: "proxy.example.com:3128", CAFile: true, DNSResolvers: []string{"system"},
		ACMEDirectory: "https://ca.example.internal/acme/directory"}}
	out, _, err := f.run(t.TempDir(), "server", "status")
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out, "proxy proxy.example.com:3128 · certificate authorities of its own · DNS system · certificates from https://ca.example.internal/acme/directory") {
		t.Errorf("no Network line:\n%s", out)
	}

	f.server.Network = &api.NetworkStatus{DNSResolvers: []string{}}
	if out, _, _ = f.run(t.TempDir(), "server", "status"); strings.Contains(out, "Network") {
		t.Errorf("a Network line with nothing to say:\n%s", out)
	}
}

func TestACAFileThatCannotBeUsedStopsTheCommandWithItsName(t *testing.T) {
	f := newFakeAgent(t)
	f.env = map[string]string{"SHIPWICK_CA_FILE": filepath.Join(t.TempDir(), "missing.pem")}
	_, _, err := f.run(t.TempDir(), "ps")
	if err == nil || !strings.HasPrefix(err.Error(), "SHIPWICK_CA_FILE: ") || !strings.Contains(err.Error(), "does not exist") {
		t.Fatalf("err = %v", err)
	}
}
