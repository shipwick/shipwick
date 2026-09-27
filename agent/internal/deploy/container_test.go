package deploy

import (
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func TestReplicasCarryTheProcessAndLoggingOverrides(t *testing.T) {
	h := newHarness(t)
	a := app("my-api", "my-api:1.0", 1)
	a.Entrypoint = []string{"dotnet"}
	a.Command = []string{"App.dll", "--urls", "http://0.0.0.0:8080"}
	a.User = "1000:1000"
	a.Logging = &spec.Logging{Driver: "gelf", Options: map[string]string{"gelf-address": "udp://logs.example.com:12201"}}

	d := h.deploy(a)
	if d.Status != api.StatusActive {
		t.Fatalf("status = %s (%s)", d.Status, d.Error)
	}
	containers := h.rt.Containers()
	if len(containers) != 1 {
		t.Fatalf("got %d containers, want 1", len(containers))
	}
	s := h.rt.Spec(containers[0].ID)
	if strings.Join(s.Entrypoint, " ") != "dotnet" || strings.Join(s.Command, " ") != "App.dll --urls http://0.0.0.0:8080" || s.User != "1000:1000" {
		t.Errorf("process overrides did not reach the container: %+v", s)
	}
	if s.LogDriver != "gelf" || s.LogOptions["gelf-address"] != "udp://logs.example.com:12201" {
		t.Errorf("logging did not reach the container: %+v", s)
	}
}

func TestReplicasWithoutOverridesLeaveTheDefaults(t *testing.T) {
	h := newHarness(t)
	h.deploy(app("my-api", "my-api:1.0", 1))
	s := h.rt.Spec(h.rt.Containers()[0].ID)
	if s.Entrypoint != nil || s.Command != nil || s.User != "" || s.LogDriver != "" || s.LogOptions != nil {
		t.Errorf("nothing was asked for, nothing must be set: %+v", s)
	}
}
