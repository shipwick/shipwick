package api

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/agent/internal/docker/dockertest"
	"github.com/shipwick/shipwick/pkg/api"
)

// How the failures of what the agent stands on reach a client: a code of
// their own and a message that says where to look, instead of INTERNAL_ERROR
// around the words of whichever call met them first.

func TestADeploymentOnAFullDiskIsAnsweredWithDiskFullAndTheConfigurationStaysOutOfIt(t *testing.T) {
	f := newFixture(t)
	if err := f.store.Fill(context.Background(), true); err != nil {
		t.Fatal(err)
	}
	// A record that needs room of its own, with a value nobody may read back.
	config := strings.Replace(validConfig, "  DATABASE_PASSWORD: hunter2\n", "  DATABASE_PASSWORD: hunter2\n  LICENCE: "+strings.Repeat("s3cr3t", 10000)+"\n", 1)

	status, body := f.do("POST", "/api/v1/applications/my-api/deploy", config)
	if status != http.StatusInsufficientStorage {
		t.Fatalf("status = %d: %.300s", status, body)
	}
	e := decodeError(t, body)
	if e.Code != api.CodeDiskFull {
		t.Errorf("code = %s, want %s", e.Code, api.CodeDiskFull)
	}
	for _, want := range []string{"The server's disk is full", "database or disk is full", "Nothing was changed", "docker system df"} {
		if !strings.Contains(e.Message, want) {
			t.Errorf("message = %q, want it to contain %q", e.Message, want)
		}
	}
	if strings.Contains(string(body), "s3cr3t") || strings.Contains(string(body), "hunter2") || strings.Contains(f.logs.String(), "s3cr3t") {
		t.Error("an env value appears in the answer or in the log")
	}

	if err := f.store.Fill(context.Background(), false); err != nil {
		t.Fatal(err)
	}
	if status, body := f.do("POST", "/api/v1/applications/my-api/deploy", config); status != http.StatusAccepted {
		t.Errorf("once there is room: status = %d: %.300s", status, body)
	}
}

func TestAnImageTheServersDiskHasNoRoomForIsAnsweredWithDiskFull(t *testing.T) {
	f := newFixture(t)
	// The daemon's own words, as it passes them on from the kernel.
	f.rt.LoadErr = errors.New("load image: write /var/lib/docker/tmp/docker-import-3141592653/blobs/sha256/0f5a: no space left on device")

	status, body := f.postArchive("/api/v1/applications/my-api/images", "application/x-tar", []byte(localImage+"\n"))
	if status != http.StatusInsufficientStorage {
		t.Fatalf("status = %d: %s", status, body)
	}
	if e := decodeError(t, body); e.Code != api.CodeDiskFull || !strings.Contains(e.Message, "no space left on device") || !strings.Contains(e.Message, "docker image prune -a") {
		t.Errorf("error = %+v", e)
	}
}

func TestARequestThatNeedsDockerIsAnsweredWithRuntimeUnavailableWhileItDoesNotAnswer(t *testing.T) {
	f := newFixture(t)
	f.rt.LayersErr = dockertest.ErrDaemonDown

	status, body := f.do("POST", "/api/v1/applications/my-api/images/missing", layersBody(layer("a")))
	if status != http.StatusServiceUnavailable {
		t.Fatalf("status = %d: %s", status, body)
	}
	e := decodeError(t, body)
	if e.Code != api.CodeRuntimeUnavailable {
		t.Errorf("code = %s, want %s", e.Code, api.CodeRuntimeUnavailable)
	}
	for _, want := range []string{"Docker does not answer on the server", "keep running", "systemctl status docker", "Cannot connect to the Docker daemon"} {
		if !strings.Contains(e.Message, want) {
			t.Errorf("message = %q, want it to contain %q", e.Message, want)
		}
	}
	// The agent itself is fine, and says so: what restarts it must not.
	if status, _ := f.doWithAuth("GET", "/api/v1/health", "", ""); status != http.StatusOK {
		t.Errorf("health = %d while Docker does not answer", status)
	}
}
