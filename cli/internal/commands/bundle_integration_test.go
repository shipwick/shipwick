//go:build integration

package commands

import (
	"net/http"
	"net/http/httptest"
	"os"
	"testing"
)

// TestServerBundleWithRealDocker makes a bundle of a release built with
// scripts/build-release.sh, with the real `docker save`, and leaves it where
// SHIPWICK_BUNDLE_OUT says, for scripts/install.sh --bundle to be tried on a
// server without a network:
//
//	sh scripts/build-release.sh v0.0.0-test
//	docker build -t ghcr.io/shipwick/agent:0.0.0-test . (and caddy, dashboard)
//	SHIPWICK_BUNDLE_DIST=$PWD/dist SHIPWICK_BUNDLE_TAG=v0.0.0-test SHIPWICK_BUNDLE_OUT=/tmp/bundle.tar.gz \
//	  go test -tags integration -run TestServerBundleWithRealDocker ./cli/internal/commands/
func TestServerBundleWithRealDocker(t *testing.T) {
	dist, tag, out := os.Getenv("SHIPWICK_BUNDLE_DIST"), os.Getenv("SHIPWICK_BUNDLE_TAG"), os.Getenv("SHIPWICK_BUNDLE_OUT")
	if dist == "" || tag == "" || out == "" {
		t.Skip("set SHIPWICK_BUNDLE_DIST, SHIPWICK_BUNDLE_TAG and SHIPWICK_BUNDLE_OUT")
	}
	release := httptest.NewServer(http.StripPrefix("/"+releaseRepo+"/releases/download/"+tag+"/", http.FileServer(http.Dir(dist))))
	defer release.Close()

	f := newFakeAgent(t)
	f.upgrade = upgradeOptions{api: release.URL, web: release.URL}
	arch := os.Getenv("SHIPWICK_BUNDLE_ARCH")
	if arch == "" {
		arch = "amd64"
	}
	// The images of a release that was never published exist only here.
	stdout, _, err := f.run(t.TempDir(), "server", "bundle", "--version", tag, "--arch", arch, "--no-pull", "-o", out)
	if err != nil {
		t.Fatalf("server bundle: %v\n%s", err, stdout)
	}
	files, _ := readBundle(t, out)
	if len(files) != 7 {
		t.Errorf("the bundle holds %d entries, want the directory and six files", len(files))
	}
	t.Log(stdout)
}
