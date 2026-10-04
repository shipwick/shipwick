package deploy

import (
	"context"
	"slices"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
)

func warnings(events []api.Event) []string {
	var out []string
	for _, e := range events {
		if e.Level == api.LevelWarn {
			out = append(out, e.Message)
		}
	}
	return out
}

func TestADeploymentSaysWhichOfItsLimitsDockerDoesNotEnforce(t *testing.T) {
	h := newHarness(t)
	h.rt.Rootless, h.rt.Unenforced = true, []string{docker.LimitMemory, docker.LimitCPU}

	d := h.deploy(app("my-api", "my-api:1.0", 1)) // 0.5 CPU, 256 MB
	if d.Status != api.StatusActive {
		t.Fatalf("the deployment goes through all the same: %+v", d)
	}
	want := "Docker on this server does not enforce resources.memory and resources.cpu: the replicas run without a limit. shipwick doctor says what the server lacks"
	if got := warnings(h.events(d.ID)); !slices.Equal(got, []string{want}) {
		t.Errorf("warnings = %q\nwant %q", got, want)
	}

	// Only what the deployment asks for and the daemon does not apply.
	h.rt.Unenforced = []string{docker.LimitCPU}
	d = h.deploy(app("my-api", "my-api:1.1", 1))
	want = "Docker on this server does not enforce resources.cpu: the replicas run without a limit. shipwick doctor says what the server lacks"
	if got := warnings(h.events(d.ID)); !slices.Equal(got, []string{want}) {
		t.Errorf("warnings = %q\nwant %q", got, want)
	}
	a := app("my-api", "my-api:1.2", 1)
	a.Resources.CPU = 0
	if got := warnings(h.events(h.deploy(a).ID)); len(got) != 0 {
		t.Errorf("warnings = %q; the deployment asks for a memory limit only, and that one is enforced", got)
	}
}

func TestADeploymentOnADaemonThatEnforcesItsLimitsSaysNothing(t *testing.T) {
	h := newHarness(t)
	if got := warnings(h.events(h.deploy(app("my-api", "my-api:1.0", 1)).ID)); len(got) != 0 {
		t.Errorf("warnings = %q", got)
	}
}

func TestTheServerSaysHowItsDockerRuns(t *testing.T) {
	h := newHarness(t)
	info, err := h.engine.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Docker == nil || info.Docker.Rootless || info.Docker.UnenforcedLimits == nil || len(info.Docker.UnenforcedLimits) != 0 {
		t.Errorf("docker = %+v; a daemon that enforces everything lists nothing, as an empty list", info.Docker)
	}

	h.rt.Rootless, h.rt.Unenforced = true, []string{docker.LimitMemory, docker.LimitCPU}
	info, err = h.engine.Server(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if info.Docker == nil || !info.Docker.Rootless || !slices.Equal(info.Docker.UnenforcedLimits, []string{api.LimitMemory, api.LimitCPU}) {
		t.Errorf("docker = %+v", info.Docker)
	}

	// An application's own numbers carry it too, next to the limits it names.
	h.deploy(app("my-api", "my-api:1.0", 1))
	m, err := h.engine.Metrics(context.Background(), "my-api")
	if err != nil {
		t.Fatal(err)
	}
	if m.MemoryLimitBytes != 256<<20 || !slices.Equal(m.UnenforcedLimits, []string{api.LimitMemory, api.LimitCPU}) {
		t.Errorf("metrics = %+v; the limit deploy.yaml asks for, and that it is not applied", m)
	}
}

func TestNoMemoryAlertAboutALimitDockerDoesNotEnforce(t *testing.T) {
	ctx := context.Background()
	s := newSampled(t)
	s.deploy(app("my-api", "my-api:1.0", 1)) // limited to 256 MB
	rec := notified(s.harness)
	round := func() {
		s.advance(30 * time.Second)
		s.engine.checkAlerts(ctx, s.now)
	}

	s.rt.Unenforced = []string{docker.LimitMemory}
	s.rt.MemoryUsed = 250 << 20
	for range 6 {
		round()
	}
	if kinds := rec.Kinds(); len(kinds) != 0 || len(s.engine.Alerts()) != 0 {
		t.Fatalf("notifications = %v, active = %q; nothing kills a replica at a limit that is not applied", kinds, alertKinds(s.engine))
	}

	// An alert raised while the limit held goes when it no longer does.
	s.rt.Unenforced = nil
	for range 4 {
		round()
	}
	if len(s.engine.Alerts()) != 1 {
		t.Fatalf("active = %q; with the limit enforced the alert is due", alertKinds(s.engine))
	}
	s.rt.Unenforced = []string{docker.LimitMemory}
	round()
	if len(s.engine.Alerts()) != 0 {
		t.Errorf("active = %q; the alert must go with the limit", alertKinds(s.engine))
	}
}
