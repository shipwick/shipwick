package store

import (
	"context"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/spec"
)

func TestTheSplunkTokenIsStoredEncryptedAndReadBackClear(t *testing.T) {
	ctx := context.Background()
	s := openTest(t)

	app := testApp("api", "nginx:1")
	app.Logging = &spec.Logging{Driver: "splunk", Options: map[string]string{"splunk-url": "https://hec.example.com:8088", "splunk-token": "12345678-aaaa-bbbb"}}
	d, err := s.CreateDeployment(ctx, app, time.Now())
	if err != nil {
		t.Fatalf("CreateDeployment: %v", err)
	}
	if app.Logging.Options["splunk-token"] != "12345678-aaaa-bbbb" || d.Spec.Logging.Options["splunk-token"] != "12345678-aaaa-bbbb" {
		t.Error("the caller's spec must keep its clear token: the engine hands it to the daemon")
	}

	raw := rawSpec(t, s, d.ID)
	if strings.Contains(raw, "12345678-aaaa-bbbb") {
		t.Errorf("stored spec holds the token in clear: %s", raw)
	}
	if !strings.Contains(raw, `"splunk-token":"enc1:`) || !strings.Contains(raw, `"splunk-url":"https://hec.example.com:8088"`) {
		t.Errorf("only the token is encrypted; the other options stay readable: %s", raw)
	}

	got, err := s.GetDeployment(ctx, d.ID)
	if err != nil {
		t.Fatalf("GetDeployment: %v", err)
	}
	if got.Spec.Logging.Options["splunk-token"] != "12345678-aaaa-bbbb" {
		t.Errorf("the token did not round trip: %+v", got.Spec.Logging)
	}

	// A record written by a release that stored the token as it was reads
	// back as it is: nothing breaks on the next start, and the next
	// deployment seals it.
	if _, err := s.db.ExecContext(ctx, `UPDATE deployments SET spec = json_set(spec, '$.logging.options."splunk-token"', 'legacy-token') WHERE id = ?`, d.ID); err != nil {
		t.Fatal(err)
	}
	got, err = s.GetDeployment(ctx, d.ID)
	if err != nil || got.Spec.Logging.Options["splunk-token"] != "legacy-token" {
		t.Errorf("a token stored in clear by an earlier release: %+v, %v", got.Spec.Logging, err)
	}
}
