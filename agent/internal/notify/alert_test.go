package notify

import (
	"encoding/json"
	"net/url"
	"testing"
	"time"
)

func TestAlertIsRenderedInEveryFormat(t *testing.T) {
	alert := Event{
		Kind: AlertRaised, Application: "my-api", Server: "vps-1",
		Message: "my-api replica 2 is at 93% of its memory limit (238 MB of 256 MB)",
		At:      time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
		Alert:   &Alert{Kind: "memory", Severity: "warning", Replica: 2},
	}
	tests := []struct {
		name, url, want string
	}{
		{"slack", "https://hooks.slack.com/services/T0/B0/x", `{"text":"my-api replica 2 is at 93% of its memory limit (238 MB of 256 MB)"}`},
		{"discord", "https://discord.com/api/webhooks/1/abc", `{"content":"my-api replica 2 is at 93% of its memory limit (238 MB of 256 MB)"}`},
		{"anything else", "https://hooks.example.com/shipwick", `{"event":"alert.raised","application":"my-api","deployment_id":null,"version":"",` +
			`"message":"my-api replica 2 is at 93% of its memory limit (238 MB of 256 MB)","at":"2026-03-01T10:00:00Z","server":"vps-1",` +
			`"alert":{"kind":"memory","severity":"warning","replica":2}}`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, _ := url.Parse(tt.url)
			body, err := json.Marshal(formatFor(u)(alert))
			if err != nil {
				t.Fatal(err)
			}
			if string(body) != tt.want {
				t.Errorf("body = %s\nwant   %s", body, tt.want)
			}
		})
	}

	// An alert about the server names no application; a cleared one says
	// what it was.
	disk := Event{Kind: AlertCleared, Message: "The server's disk is back to 78% full", Alert: &Alert{Kind: "disk", Severity: "critical"}}
	body, _ := json.Marshal(newPayload(disk))
	var got struct {
		Event, Application string
		Alert              *Alert
	}
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatal(err)
	}
	if got.Event != "alert.cleared" || got.Application != "" || got.Alert == nil || *got.Alert != (Alert{Kind: "disk", Severity: "critical"}) {
		t.Errorf("payload = %s", body)
	}
}
