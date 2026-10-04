package deploy

import (
	"context"
	"slices"
	"testing"

	"github.com/shipwick/shipwick/pkg/spec"
)

func TestServerViewNamesWhatRunsWithoutAMemoryLimit(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.deploy(app("limited", "limited:1.0", 1))
	for _, name := range []string{"web", "db", "idle"} {
		a := app(name, name+":1.0", 1)
		a.Resources = spec.Resources{CPU: 0.5}
		h.deploy(a)
	}
	if err := h.engine.Stop(ctx, "idle"); err != nil {
		t.Fatal(err)
	}

	s, err := h.engine.Server(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if want := []string{"db", "web"}; !slices.Equal(s.UnlimitedMemory, want) {
		t.Errorf("unlimited_memory = %v, want %v: the running ones, in order; a stopped application uses no memory", s.UnlimitedMemory, want)
	}
}

func TestServerViewReportsSwapWhereItCanBeRead(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	if s, _ := h.engine.Server(ctx); s.SwapBytes != nil {
		t.Errorf("swap_bytes = %d where nothing can say", *s.SwapBytes)
	}
	h.engine.opts.SwapBytes = func() (int64, bool) { return 0, false }
	if s, _ := h.engine.Server(ctx); s.SwapBytes != nil {
		t.Errorf("swap_bytes = %d where the kernel would not say", *s.SwapBytes)
	}
	// No swap is a fact, and not the same as not knowing.
	h.engine.opts.SwapBytes = func() (int64, bool) { return 0, true }
	if s, _ := h.engine.Server(ctx); s.SwapBytes == nil || *s.SwapBytes != 0 {
		t.Errorf("swap_bytes = %v, want 0", s.SwapBytes)
	}
}

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
