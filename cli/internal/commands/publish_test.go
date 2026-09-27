package commands

import (
	"testing"

	"github.com/shipwick/shipwick/pkg/spec"
)

func TestDescribeSpecListsPublishedPorts(t *testing.T) {
	app, err := spec.Parse([]byte("name: db\nimage: postgres:17\ndeploy:\n  strategy: recreate\npublish:\n  - port: 5432\n    host: 15432\n    address: 10.0.0.5\n  - port: 5353\n    protocol: udp\n"))
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range describeSpec(app) {
		if f[0] == "Publish" {
			got = append(got, f[1])
		}
	}
	want := []string{"5432/tcp → server port 15432 on 10.0.0.5", "5353/udp → server port 5353"}
	if len(got) != len(want) || got[0] != want[0] || got[1] != want[1] {
		t.Errorf("Publish lines = %q, want %q", got, want)
	}
}
