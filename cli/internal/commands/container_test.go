package commands

import (
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/spec"
)

func TestDescribeArgvKeepsArgumentBoundariesVisible(t *testing.T) {
	got := describeArgv([]string{"sh", "-c", "echo hi", "", `say "x"`})
	if want := `sh -c "echo hi" "" "say \"x\""`; got != want {
		t.Errorf("describeArgv = %s, want %s", got, want)
	}
}

func TestDescribeSpecShowsProcessAndLogging(t *testing.T) {
	a := spec.App{Name: "api", Image: "app:1", Replicas: 1}
	a.Entrypoint = []string{"dotnet"}
	a.Command = []string{"App.dll", "--urls", "http://0.0.0.0:8080"}
	a.User = "1000:1000"
	a.Logging = &spec.Logging{Driver: "gelf", Options: map[string]string{"gelf-address": "udp://h:1", "tag": "x"}}

	fields := map[string]string{}
	for _, f := range describeSpec(a) {
		fields[f[0]] = f[1]
	}
	want := map[string]string{
		"Entrypoint": "dotnet",
		"Command":    "App.dll --urls http://0.0.0.0:8080",
		"User":       "1000:1000",
		"Logging":    "gelf (2 options)",
	}
	for k, v := range want {
		if fields[k] != v {
			t.Errorf("%s = %q, want %q", k, fields[k], v)
		}
	}

	plain := strings.Join(flatten(describeSpec(spec.App{Name: "api", Image: "app:1", Replicas: 1})), "\n")
	for _, k := range []string{"Entrypoint", "Command", "User", "Logging"} {
		if strings.Contains(plain, k) {
			t.Errorf("%s shown for an application that did not set it:\n%s", k, plain)
		}
	}
	if got := describeLogging(spec.Logging{Driver: "local", Options: map[string]string{"max-size": "50m"}}); got != "local (1 option)" {
		t.Errorf("describeLogging = %q", got)
	}
}

func flatten(fields [][2]string) []string {
	out := make([]string, len(fields))
	for i, f := range fields {
		out[i] = f[0] + ": " + f[1]
	}
	return out
}
