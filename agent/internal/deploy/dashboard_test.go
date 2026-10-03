package deploy

import (
	"context"
	"testing"
)

func TestServerViewReportsWhereTheDashboardIs(t *testing.T) {
	h := newHarness(t)
	if s, _ := h.engine.Server(context.Background()); s.DashboardURL != "" {
		t.Errorf("no dashboard hostname configured, but the server view says %q", s.DashboardURL)
	}
	h.engine.opts.DashboardURL = "https://dashboard.example.com"
	if s, _ := h.engine.Server(context.Background()); s.DashboardURL != "https://dashboard.example.com" {
		t.Errorf("dashboard_url = %q", s.DashboardURL)
	}
}
