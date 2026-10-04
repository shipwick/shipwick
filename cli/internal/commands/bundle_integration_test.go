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
//
// With SHIPWICK_BUNDLE_PULL set the images are pulled from the registry and
// held against the release's digests: for a dist whose compose file names
// images that are published, and whose image-digests.txt (written with
// scripts/image-digests.sh, and added to checksums.txt as the release
// workflow adds it) says what they are. DOCKER_HOST chooses the daemon, and
// with it how the archive is written: the classic image store, or
// containerd's.
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
	args, want := []string{"server", "bundle", "--version", tag, "--arch", arch, "-o", out, "--no-pull"}, 7
	if os.Getenv("SHIPWICK_BUNDLE_PULL") != "" {
		// And image-digests.txt.
		args, want = args[:len(args)-1], 8
	}
	stdout, stderr, err := f.run(t.TempDir(), args...)
	if err != nil {
		t.Fatalf("server bundle: %v\n%s", err, stdout)
	}
	files, _ := readBundle(t, out)
	if len(files) != want {
		t.Errorf("the bundle holds %d entries, want the directory and %d files", len(files), want-1)
	}
	t.Log(stdout, stderr)
}
