package commands

import (
	"testing"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/api"
)

func TestAFullDiskIsRenderedWithTheAgentsWordsAndWhereToLook(t *testing.T) {
	msg := "The server's disk is full (create deployment: database or disk is full (13)). Nothing was changed. Free space on the server — docker system df shows what takes it, docker image prune -a removes images nothing uses — and try again"
	got := Render(&client.APIError{Status: 507, Code: api.CodeDiskFull, Message: msg})
	if want := "Error: " + msg + ".\n\nSee how full the disk is with: shipwick server status"; got != want {
		t.Errorf("Render:\n got %q\nwant %q", got, want)
	}
}

func TestDockerNotAnsweringIsRenderedAsTheAgentSaysIt(t *testing.T) {
	msg := "Docker does not answer on the server. Applications that are running keep running; look at the daemon there with: systemctl status docker. The cause: no answer within 15s"
	got := Render(&client.APIError{Status: 503, Code: api.CodeRuntimeUnavailable, Message: msg})
	if want := "Error: " + msg; got != want {
		t.Errorf("Render:\n got %q\nwant %q", got, want)
	}
}
