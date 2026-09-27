package commands

import (
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestServerStatusShowsNotifications(t *testing.T) {
	f := newFakeAgent(t)
	f.server = api.Server{AgentVersion: "1.2.3", Hostname: "vps-1", Notifications: api.NotificationStatus{Webhook: true}}
	out, _, err := f.run(t.TempDir(), "server", "status")
	if err != nil {
		t.Fatalf("server status: %v", err)
	}
	assertInOrder(t, out, []string{"Proxy", "Notifications", "webhook configured"})

	// An agent without notifications, or an older one that does not know the field.
	f.server.Notifications = api.NotificationStatus{}
	out, _, _ = f.run(t.TempDir(), "server", "status")
	assertInOrder(t, out, []string{"Notifications", "none", "SHIPWICK_WEBHOOK_URL"})
}
