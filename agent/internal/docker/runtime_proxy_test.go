package docker

import "testing"

func TestTheComposeProjectIsReadOffTheAgentsOwnLabels(t *testing.T) {
	for _, tc := range []struct {
		name   string
		labels map[string]string
		want   string
	}{
		{"an installation under another project name", map[string]string{composeProjectLabel: "acme-deploy", composeServiceLabel: "agent"}, "acme-deploy"},
		{"the shipped compose files", map[string]string{composeProjectLabel: "shipwick"}, "shipwick"},
		{"a container Compose did not start", map[string]string{"maintainer": "someone"}, defaultComposeProject},
		{"not a container at all", nil, defaultComposeProject},
	} {
		if got := projectOf(tc.labels); got != tc.want {
			t.Errorf("%s: project = %q, want %q", tc.name, got, tc.want)
		}
	}
}
