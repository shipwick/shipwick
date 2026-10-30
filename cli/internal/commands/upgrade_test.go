package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"sync"
	"testing"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/pkg/api"
)

// fakeGitHub answers the two GitHub endpoints `shipwick upgrade` uses: the
// releases API and the website's release downloads.
type fakeGitHub struct {
	srv *httptest.Server

	mu          sync.Mutex
	tag         string
	rateLimited bool   // the API answers 403, as it does after 60 anonymous calls
	binary      []byte // the linux/amd64 asset of the release
	checksums   string // checksums.txt; defaults to the binary's real hash
	requests    []string
	// extra are other assets of the release, by name.
	extra map[string][]byte
}

func newFakeGitHub(t *testing.T) *fakeGitHub {
	t.Helper()
	g := &fakeGitHub{tag: "v0.3.0", binary: []byte("new binary v0.3.0\n")}
	mux := http.NewServeMux()
	mux.HandleFunc("GET /repos/shipwick/shipwick/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		if g.rateLimited {
			w.WriteHeader(http.StatusForbidden)
			fmt.Fprint(w, `{"message":"API rate limit exceeded"}`)
			return
		}
		fmt.Fprintf(w, `{"tag_name":%q,"prerelease":false}`, g.tag)
	})
	mux.HandleFunc("GET /shipwick/shipwick/releases/latest", func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, g.srv.URL+"/shipwick/shipwick/releases/tag/"+g.tag, http.StatusFound)
	})
	mux.HandleFunc("GET /shipwick/shipwick/releases/download/{tag}/{asset}", func(w http.ResponseWriter, r *http.Request) {
		if r.PathValue("tag") != g.tag {
			http.NotFound(w, r)
			return
		}
		switch r.PathValue("asset") {
		case "checksums.txt":
			sums := g.checksums
			if sums == "" {
				sum := sha256.Sum256(g.binary)
				sums = hex.EncodeToString(sum[:]) + "  shipwick_linux_amd64\n"
			}
			fmt.Fprint(w, sums)
		case "shipwick_linux_amd64":
			w.Write(g.binary)
		default:
			if content, ok := g.extra[r.PathValue("asset")]; ok {
				w.Write(content)
				return
			}
			http.NotFound(w, r)
		}
	})
	g.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		g.mu.Lock()
		g.requests = append(g.requests, r.URL.Path)
		g.mu.Unlock()
		if !strings.HasPrefix(r.UserAgent(), "shipwick/") {
			t.Errorf("GitHub asks for a User-Agent naming the client, got %q", r.UserAgent())
		}
		mux.ServeHTTP(w, r)
	}))
	t.Cleanup(g.srv.Close)
	return g
}

func (g *fakeGitHub) downloads() int {
	g.mu.Lock()
	defer g.mu.Unlock()
	n := 0
	for _, p := range g.requests {
		if strings.Contains(p, "/releases/download/") {
			n++
		}
	}
	return n
}

// installed writes a stand-in for the running binary and returns its path.
func installed(t *testing.T) string {
	t.Helper()
	exe := filepath.Join(t.TempDir(), "shipwick")
	if err := os.WriteFile(exe, []byte("old binary v0.2.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	return exe
}

// upgradeAgent is a fake agent whose `shipwick upgrade` looks at g and
// believes it runs current at exe.
func upgradeAgent(t *testing.T, g *fakeGitHub, exe, current string) *fakeAgent {
	f := newFakeAgent(t)
	f.upgrade = upgradeOptions{
		api:        g.srv.URL,
		web:        g.srv.URL,
		executable: func() (string, error) { return exe, nil },
		goos:       "linux",
		goarch:     "amd64",
		version:    current,
	}
	return f
}

func TestUpgradeReplacesTheBinary(t *testing.T) {
	g := newFakeGitHub(t)
	exe := installed(t)
	f := upgradeAgent(t, g, exe, "v0.2.0")

	out, _, err := f.run(t.TempDir(), "upgrade")
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Upgraded shipwick v0.2.0 → v0.3.0", exe, "The server runs 1.2.3, ahead of the latest release (v0.3.0)"})
	if strings.Contains(out, "/releases/download/") {
		t.Errorf("download URLs belong in failures only:\n%s", out)
	}
	got, _ := os.ReadFile(exe)
	if string(got) != string(g.binary) {
		t.Errorf("the binary was not replaced, still holds %q", got)
	}
	if runtime.GOOS != "windows" {
		if info, _ := os.Stat(exe); info.Mode().Perm() != 0o755 {
			t.Errorf("the new binary should keep the old one's mode, got %o", info.Mode().Perm())
		}
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), ".shipwick-new")); err == nil {
		t.Error("the staging file must not be left behind")
	}
	if runtime.GOOS == "windows" {
		if _, err := os.Stat(staleExecutableName(exe)); err != nil {
			t.Error("on Windows the old binary is parked as .old.exe for the next run to remove")
		}
	}
}

func TestUpgradeRefusesAChecksumMismatch(t *testing.T) {
	g := newFakeGitHub(t)
	g.checksums = strings.Repeat("0", 64) + "  shipwick_linux_amd64\n"
	exe := installed(t)
	f := upgradeAgent(t, g, exe, "v0.2.0")

	_, _, err := f.run(t.TempDir(), "upgrade")
	if err == nil || !strings.Contains(err.Error(), "does not match the checksum") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v, want a refusal naming the checksum", err)
	}
	if !strings.Contains(err.Error(), "/releases/download/") {
		t.Error("a failure should say where the download came from")
	}
	got, _ := os.ReadFile(exe)
	if string(got) != "old binary v0.2.0\n" {
		t.Errorf("the binary must be untouched after a mismatch, holds %q", got)
	}
	if _, err := os.Stat(filepath.Join(filepath.Dir(exe), ".shipwick-new")); err == nil {
		t.Error("the rejected download must not be left behind")
	}
}

func TestUpgradeMissingAssetChangesNothing(t *testing.T) {
	g := newFakeGitHub(t)
	g.checksums = strings.Repeat("a", 64) + "  shipwick_darwin_arm64\n"
	exe := installed(t)
	f := upgradeAgent(t, g, exe, "v0.2.0")

	_, _, err := f.run(t.TempDir(), "upgrade")
	if err == nil || !strings.Contains(err.Error(), "has no shipwick_linux_amd64") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary v0.2.0\n" {
		t.Error("the binary must be untouched")
	}
}

func TestUpgradeCheckOnlyReports(t *testing.T) {
	g := newFakeGitHub(t)
	exe := installed(t)
	f := upgradeAgent(t, g, exe, "v0.2.0")

	out, _, err := f.run(t.TempDir(), "upgrade", "--check")
	if err != nil {
		t.Fatalf("upgrade --check: %v", err)
	}
	assertInOrder(t, out, []string{"shipwick v0.2.0 is installed; v0.3.0 is available", "Upgrade with: shipwick upgrade"})
	if g.downloads() != 0 {
		t.Errorf("--check must download nothing, saw %v", g.requests)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary v0.2.0\n" {
		t.Error("--check must not touch the binary")
	}
}

func TestUpgradeUpToDate(t *testing.T) {
	g := newFakeGitHub(t)
	f := upgradeAgent(t, g, "", "v0.3.0")
	f.upgrade.executable = func() (string, error) { t.Error("an up-to-date binary need not be located"); return "", nil }

	out, _, err := f.run(t.TempDir(), "upgrade")
	if err != nil || !strings.Contains(out, "shipwick v0.3.0 is up to date") {
		t.Errorf("err = %v, out:\n%s", err, out)
	}
	if g.downloads() != 0 {
		t.Errorf("nothing to download, saw %v", g.requests)
	}
}

func TestUpgradeNeverPicksAPrereleaseOrDowngrades(t *testing.T) {
	g := newFakeGitHub(t)
	g.tag = "v0.1.0"
	f := upgradeAgent(t, g, installed(t), "v0.2.0")
	out, _, err := f.run(t.TempDir(), "upgrade")
	if err != nil || !strings.Contains(out, "is up to date") {
		t.Errorf("a binary newer than the latest release is up to date; err = %v, out:\n%s", err, out)
	}

	f = upgradeAgent(t, g, installed(t), "dev")
	out, _, err = f.run(t.TempDir(), "upgrade")
	if err != nil || !strings.Contains(out, "development build") || g.downloads() != 0 {
		t.Errorf("a development build is reported, not replaced; err = %v, out:\n%s", err, out)
	}
}

func TestUpgradeFallsBackWhenTheAPIIsRateLimited(t *testing.T) {
	g := newFakeGitHub(t)
	g.rateLimited = true
	f := upgradeAgent(t, g, installed(t), "v0.2.0")

	out, _, err := f.run(t.TempDir(), "upgrade", "--check")
	if err != nil || !strings.Contains(out, "v0.3.0 is available") {
		t.Errorf("the redirect should still name the release; err = %v, out:\n%s", err, out)
	}
	if !strings.Contains(strings.Join(g.requests, " "), "/shipwick/shipwick/releases/latest") {
		t.Errorf("expected the website fallback, saw %v", g.requests)
	}
}

func TestUpgradeLeavesPackageManagersAlone(t *testing.T) {
	tests := []struct{ exe, want string }{
		{"/opt/homebrew/Cellar/shipwick/0.2.0/bin/shipwick", "brew upgrade shipwick"},
		{"/home/linuxbrew/.linuxbrew/Cellar/shipwick/0.2.0/bin/shipwick", "brew upgrade shipwick"},
	}
	for _, tt := range tests {
		g := newFakeGitHub(t)
		f := upgradeAgent(t, g, tt.exe, "v0.2.0")
		out, _, err := f.run(t.TempDir(), "upgrade")
		if err != nil || !strings.Contains(out, tt.want) || !strings.Contains(out, "v0.3.0 is available") {
			t.Errorf("%s: err = %v, out:\n%s", tt.exe, err, out)
		}
		if g.downloads() != 0 {
			t.Errorf("%s: a package manager's binary must not be replaced, saw %v", tt.exe, g.requests)
		}
	}
}

func TestInstallMethod(t *testing.T) {
	tests := []struct{ exe, want string }{
		{"/usr/local/bin/shipwick", ""},
		{"/home/me/.local/bin/shipwick", ""},
		{`C:\Users\me\bin\shipwick.exe`, ""},
		{"/opt/homebrew/Cellar/shipwick/0.2.0/bin/shipwick", "brew"},
		{"/usr/local/Cellar/shipwick/0.2.0/bin/shipwick", "brew"},
		{"/home/linuxbrew/.linuxbrew/Cellar/shipwick/0.2.0/bin/shipwick", "brew"},
		{`C:\Users\me\AppData\Local\Programs\shipwick\shipwick.exe`, ""},
	}
	for _, tt := range tests {
		if got := installMethod(tt.exe); got != tt.want {
			t.Errorf("installMethod(%q) = %q, want %q", tt.exe, got, tt.want)
		}
	}
	if got := staleExecutableName(`C:\bin\shipwick.exe`); got != `C:\bin\shipwick.old.exe` {
		t.Errorf("staleExecutableName = %q", got)
	}
}

func TestUpgradeRefusesAnUnwritableDirectory(t *testing.T) {
	if runtime.GOOS == "windows" || os.Geteuid() == 0 {
		t.Skip("needs Unix mode bits and a user they apply to")
	}
	g := newFakeGitHub(t)
	exe := installed(t)
	if err := os.Chmod(filepath.Dir(exe), 0o555); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Chmod(filepath.Dir(exe), 0o755) })
	f := upgradeAgent(t, g, exe, "v0.2.0")

	_, _, err := f.run(t.TempDir(), "upgrade")
	if err == nil || !strings.Contains(err.Error(), "sudo shipwick upgrade") {
		t.Fatalf("err = %v, want a refusal suggesting sudo", err)
	}
	if g.downloads() != 0 {
		t.Errorf("nothing should be downloaded into a directory that cannot be written, saw %v", g.requests)
	}
}

func TestUpgradeReportsAnOlderServer(t *testing.T) {
	g := newFakeGitHub(t)
	f := upgradeAgent(t, g, installed(t), "v0.3.0")
	f.health = api.Health{Status: "ok", Version: "v0.2.0"}

	out, _, err := f.run(t.TempDir(), "upgrade")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"is up to date", "The server runs v0.2.0; v0.3.0 is available. On the server run:", "curl -fsSL https://get.shipwick.com | sh"})
}

func TestUpgradeSkipsAnUnreachableServerQuietly(t *testing.T) {
	g := newFakeGitHub(t)
	f := upgradeAgent(t, g, installed(t), "v0.3.0")
	f.env = map[string]string{cliconfig.EnvURL: "http://127.0.0.1:1"}

	out, _, err := f.run(t.TempDir(), "upgrade", "--check")
	if err != nil {
		t.Fatalf("an unreachable server must not fail the upgrade: %v", err)
	}
	lines := strings.Split(strings.TrimSpace(out), "\n")
	last := lines[len(lines)-1]
	if !strings.Contains(last, "could not be reached") || !strings.Contains(last, "http://127.0.0.1:1") {
		t.Errorf("expected one line about the server, got:\n%s", out)
	}
}

func TestUpgradeVersionInstallsExactlyThatRelease(t *testing.T) {
	g := newFakeGitHub(t)
	g.tag = "v0.4.0-rc.1"
	exe := installed(t)
	f := upgradeAgent(t, g, exe, "v0.3.0")

	out, _, err := f.run(t.TempDir(), "upgrade", "--version", "v0.4.0-rc.1")
	if err != nil {
		t.Fatalf("upgrade --version: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Upgraded shipwick v0.3.0 → v0.4.0-rc.1", exe, "The server runs 1.2.3."})
	if strings.Contains(out, "latest release") {
		t.Errorf("a pinned release is not compared with the latest:\n%s", out)
	}
	if got, _ := os.ReadFile(exe); string(got) != string(g.binary) {
		t.Errorf("the binary was not replaced, still holds %q", got)
	}
	if strings.Contains(strings.Join(g.requests, " "), "/releases/latest") {
		t.Errorf("--version has no use for the latest release, asked: %v", g.requests)
	}

	// Without the "v", and going back: a choice, not a comparison.
	exe = installed(t)
	f = upgradeAgent(t, g, exe, "v0.5.0")
	out, _, err = f.run(t.TempDir(), "upgrade", "--version", "0.4.0-rc.1")
	if err != nil || !strings.Contains(out, "Installed shipwick v0.4.0-rc.1 in place of v0.5.0") {
		t.Errorf("err = %v, out:\n%s", err, out)
	}
	if got, _ := os.ReadFile(exe); string(got) != string(g.binary) {
		t.Errorf("the binary was not replaced, still holds %q", got)
	}
}

func TestUpgradeVersionTellsTheServerWhichReleaseToInstall(t *testing.T) {
	g := newFakeGitHub(t)
	g.tag = "v0.4.0-rc.1"
	f := upgradeAgent(t, g, installed(t), "v0.3.0")
	f.health = api.Health{Status: "ok", Version: "v0.3.0"}

	out, _, err := f.run(t.TempDir(), "upgrade", "--version", "v0.4.0-rc.1")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"The server runs v0.3.0; v0.4.0-rc.1 is available. On the server run:", "curl -fsSL https://get.shipwick.com | SHIPWICK_VERSION=v0.4.0-rc.1 sh"})
}

func TestUpgradeVersionLeavesTheSameReleaseAlone(t *testing.T) {
	g := newFakeGitHub(t)
	g.tag = "v0.4.0-rc.1"
	f := upgradeAgent(t, g, "", "v0.4.0-rc.1")
	f.upgrade.executable = func() (string, error) { t.Error("the release that runs need not be located"); return "", nil }

	out, _, err := f.run(t.TempDir(), "upgrade", "--version", "v0.4.0-rc.1")
	if err != nil || !strings.Contains(out, "shipwick v0.4.0-rc.1 is installed already") {
		t.Errorf("err = %v, out:\n%s", err, out)
	}
	if g.downloads() != 0 {
		t.Errorf("nothing to download, saw %v", g.requests)
	}
}

func TestUpgradeVersionCheckOnlyReports(t *testing.T) {
	g := newFakeGitHub(t)
	g.tag = "v0.4.0-rc.1"
	exe := installed(t)
	f := upgradeAgent(t, g, exe, "v0.3.0")

	out, _, err := f.run(t.TempDir(), "upgrade", "--version", "v0.4.0-rc.1", "--check")
	if err != nil {
		t.Fatal(err)
	}
	assertInOrder(t, out, []string{"shipwick v0.3.0 is installed.", "Install v0.4.0-rc.1 with: shipwick upgrade --version v0.4.0-rc.1"})
	if g.downloads() != 0 || string(must(os.ReadFile(exe))) != "old binary v0.2.0\n" {
		t.Errorf("--check must change nothing, saw %v", g.requests)
	}
}

func must(b []byte, err error) []byte { return b }

func TestUpgradeVersionRefusesWhatIsNotARelease(t *testing.T) {
	g := newFakeGitHub(t)
	for _, bad := range []string{"latest", "v0.4", "v0.4.0-rc.1/../x", "v0.8.1-3-gabcdef"} {
		f := upgradeAgent(t, g, installed(t), "v0.3.0")
		_, _, err := f.run(t.TempDir(), "upgrade", "--version", bad)
		if err == nil || !strings.Contains(err.Error(), "is not a release version") {
			t.Errorf("--version %q: err = %v", bad, err)
		}
	}
	if len(g.requests) != 0 {
		t.Errorf("nothing should be asked of GitHub for a tag that is not a version, saw %v", g.requests)
	}

	// A version that is not a release: GitHub has no files for it.
	f := upgradeAgent(t, g, installed(t), "v0.3.0")
	_, _, err := f.run(t.TempDir(), "upgrade", "--version", "v9.9.9")
	if err == nil || !strings.Contains(err.Error(), "release v9.9.9 was not found") || !strings.Contains(err.Error(), "/releases") {
		t.Errorf("err = %v, want a refusal naming the release", err)
	}
}

func TestUpgradeVersionVerifiesThePrereleasesOwnSignature(t *testing.T) {
	g, cosign := signedGitHub(t, "v0.9.0-rc.1"), &fakeCosign{}
	exe := installed(t)
	f := upgradeAgent(t, g, exe, "v0.8.1")
	f.upgrade.cosign, f.upgrade.run = cosign.path, cosign.run

	out, _, err := f.run(t.TempDir(), "upgrade", "--version", "v0.9.0-rc.1")
	if err != nil {
		t.Fatalf("upgrade --version: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Upgraded shipwick v0.8.1 → v0.9.0-rc.1", "Release v0.9.0-rc.1 is signed by the release workflow"})
	if len(cosign.calls) != 1 || !slices.Contains(cosign.calls[0], signatureIdentity("v0.9.0-rc.1")) {
		t.Errorf("cosign was run as %v, want the identity of the workflow at the pre-release's tag", cosign.calls)
	}

	// One whose signature was taken away is refused like a release.
	g.extra = nil
	exe = installed(t)
	f = upgradeAgent(t, g, exe, "v0.8.1")
	f.upgrade.cosign, f.upgrade.run = cosign.path, cosign.run
	_, _, err = f.run(t.TempDir(), "upgrade", "--version", "v0.9.0-rc.1")
	if err == nil || !strings.Contains(err.Error(), "release v0.9.0-rc.1 has no signature") {
		t.Errorf("err = %v, want a refusal naming the missing signature", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary v0.2.0\n" {
		t.Errorf("the binary must be untouched, holds %q", got)
	}
}

func TestUpgradeVersionLeavesPackageManagersAlone(t *testing.T) {
	g := newFakeGitHub(t)
	g.tag = "v0.4.0-rc.1"
	f := upgradeAgent(t, g, "/opt/homebrew/Cellar/shipwick/0.3.0/bin/shipwick", "v0.3.0")
	out, _, err := f.run(t.TempDir(), "upgrade", "--version", "v0.4.0-rc.1")
	if err != nil || !strings.Contains(out, "was installed with Homebrew and is left to it") || !strings.Contains(out, "/releases/tag/v0.4.0-rc.1") {
		t.Errorf("err = %v, out:\n%s", err, out)
	}
	if g.downloads() != 0 {
		t.Errorf("a package manager's binary must not be replaced, saw %v", g.requests)
	}
}

func TestAPrereleaseAheadOfTheLatestReleaseIsCurrent(t *testing.T) {
	g := newFakeGitHub(t)
	g.tag = "v0.8.1"
	f := upgradeAgent(t, g, installed(t), "v0.9.0-rc.1")
	out, _, err := f.run(t.TempDir(), "upgrade")
	if err != nil || !strings.Contains(out, "shipwick v0.9.0-rc.1 is up to date") || g.downloads() != 0 {
		t.Errorf("err = %v, out:\n%s", err, out)
	}
	out, _, err = f.run(t.TempDir(), "upgrade", "--check")
	if err != nil || strings.Contains(out, "is available") {
		t.Errorf("--check must not offer the release a pre-release precedes; err = %v, out:\n%s", err, out)
	}
}

func TestABuildPastATagIsADevelopmentBuild(t *testing.T) {
	g := newFakeGitHub(t)
	g.tag = "v0.8.1"
	exe := installed(t)
	f := upgradeAgent(t, g, exe, "v0.8.1-3-gabcdef")
	out, _, err := f.run(t.TempDir(), "upgrade")
	if err != nil || !strings.Contains(out, "This shipwick is a development build (v0.8.1-3-gabcdef); the latest release is v0.8.1") {
		t.Errorf("err = %v, out:\n%s", err, out)
	}
	if g.downloads() != 0 || string(must(os.ReadFile(exe))) != "old binary v0.2.0\n" {
		t.Errorf("a build made after the tag must not be replaced by the tag, saw %v", g.requests)
	}
}
