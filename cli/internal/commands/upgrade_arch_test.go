package commands

import (
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func checksumLine(name string, content []byte) string {
	sum := sha256.Sum256(content)
	return hex.EncodeToString(sum[:]) + "  " + name + "\n"
}

// windowsRelease is a release with the two Windows builds, and a shipwick
// installed as shipwick.exe that believes it runs on Windows.
func windowsRelease(t *testing.T, goarch, machine string, assets ...string) (*fakeGitHub, *fakeAgent, string) {
	t.Helper()
	g := newFakeGitHub(t)
	g.extra = map[string][]byte{}
	for _, asset := range assets {
		g.extra[asset] = []byte("build " + asset + "\n")
		g.checksums += checksumLine(asset, g.extra[asset])
	}
	exe := filepath.Join(t.TempDir(), "shipwick.exe")
	if err := os.WriteFile(exe, []byte("old binary v0.2.0\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	f := upgradeAgent(t, g, exe, "v0.2.0")
	f.upgrade.goos, f.upgrade.goarch, f.upgrade.machine = "windows", goarch, machine
	return g, f, exe
}

func TestUpgradeOnWindowsPicksTheBuildForTheMachine(t *testing.T) {
	both := []string{"shipwick_windows_amd64.exe", "shipwick_windows_arm64.exe"}
	cases := map[string]struct {
		goarch, machine string
		assets          []string
		want            string
	}{
		"an arm64 build on arm64":                              {"arm64", "arm64", both, "shipwick_windows_arm64.exe"},
		"an amd64 build on amd64":                              {"amd64", "amd64", both, "shipwick_windows_amd64.exe"},
		"an amd64 build that runs on arm64":                    {"amd64", "arm64", both, "shipwick_windows_arm64.exe"},
		"an amd64 build on arm64, a release without the other": {"amd64", "arm64", both[:1], "shipwick_windows_amd64.exe"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			g, f, exe := windowsRelease(t, tc.goarch, tc.machine, tc.assets...)
			if out, _, err := f.run(t.TempDir(), "upgrade"); err != nil {
				t.Fatalf("upgrade: %v\n%s", err, out)
			}
			if got, _ := os.ReadFile(exe); string(got) != string(g.extra[tc.want]) {
				t.Errorf("installed %q, want the content of %s", got, tc.want)
			}
		})
	}
}

func TestUpgradeNamesTheBuildAReleaseDoesNotHave(t *testing.T) {
	_, f, exe := windowsRelease(t, "arm64", "arm64", "shipwick_windows_amd64.exe")
	_, _, err := f.run(t.TempDir(), "upgrade")
	if err == nil || !strings.Contains(err.Error(), "release v0.3.0 has no shipwick_windows_arm64.exe") || !strings.Contains(err.Error(), "nothing was changed") {
		t.Fatalf("err = %v", err)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary v0.2.0\n" {
		t.Errorf("the binary was changed: %q", got)
	}
}

func TestTheMachinesArchitectureIsOneTheReleaseIsBuiltFor(t *testing.T) {
	arch := machineArch()
	if arch != "amd64" && arch != "arm64" {
		t.Skipf("this machine is %s", arch)
	}
	// Emulation aside, the machine is what the test binary was built for.
	if runtime.GOOS != "windows" && arch != runtime.GOARCH {
		t.Errorf("machineArch() = %s, built for %s", arch, runtime.GOARCH)
	}
}
