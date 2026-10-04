package deploy

import (
	"context"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func withSecurity(a spec.App, s spec.Security) spec.App {
	a.Security = &s
	return a
}

// Every container made from the application's spec, not only its replicas: a
// job that could write the root filesystem or run as root would be the way
// around the block.
func TestSecurityReachesEveryContainerOfTheApplication(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	withBackups(h, "", nil)
	specs := createdSpecs(h)

	a := backupApp(spec.Backups{Schedule: "0 3 * * *", Before: []string{"pg_dump", "-f", "/var/lib/data/backup.sql"}, BeforeIn: spec.BeforeInContainer})
	a = withJob(withHook(a, "psql", "-f", "migrate.sql"), "nightly", "0 3 * * *", time.Minute, "vacuumdb", "--all")
	a.User = "70:70"
	a = withSecurity(a, spec.Security{
		ReadOnly:     true,
		Tmpfs:        []spec.Tmpfs{{Path: "/var/run/postgresql", SizeBytes: 16 << 20}, {Path: "/tmp", SizeBytes: spec.DefaultTmpfsBytes}},
		Capabilities: &[]string{},
		NonRoot:      true,
	})
	if d := h.deploy(a); d.Status != api.StatusActive {
		t.Fatalf("deployment: %s (%s)", d.Status, d.Error)
	}
	if _, err := h.engine.RunJob(ctx, a.Name, "nightly"); err != nil {
		t.Fatalf("RunJob: %v", err)
	}
	h.engine.Wait()
	if _, err := h.engine.RunCommand(ctx, a.Name, []string{"psql", "-c", "select 1"}); err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	h.engine.Wait()
	if run := h.backup(a.Name); run.Status != api.BackupSucceeded {
		t.Fatalf("backup: %+v", run)
	}
	if got := h.verify(a.Name, 1); got.VerifiedAt == nil {
		t.Fatalf("verification: %+v", got)
	}

	want := &docker.Security{
		ReadOnly:         true,
		Tmpfs:            []docker.Tmpfs{{Path: "/var/run/postgresql", SizeBytes: 16 << 20}, {Path: "/tmp", SizeBytes: 64 << 20}},
		DropCapabilities: true,
		NonRoot:          true,
	}
	want.Capabilities = []string{} // none kept, as the stored spec says it
	got := specs()
	for _, kind := range []string{"replica", "pre-deploy", "nightly", "run", beforeJobName, docker.VerifyJob} {
		s, created := got[kind]
		switch {
		case !created:
			t.Errorf("no %s container was created: %v", kind, got)
		case !reflect.DeepEqual(s.Security, want):
			t.Errorf("the %s container has %+v, want what the replicas have: %+v", kind, s.Security, want)
		case s.User != "70:70":
			t.Errorf("the %s container runs as %q, want the application's user", kind, s.User)
		}
	}
}

func TestCapabilitiesAreDroppedOnlyWhenTheBlockNamesThem(t *testing.T) {
	h := newHarness(t)
	specs := createdSpecs(h)

	h.deploy(withSecurity(app("api", "api:1.0", 1), spec.Security{ReadOnly: true}))
	if s := specs()["replica"].Security; s == nil || !s.ReadOnly || s.DropCapabilities || s.Capabilities != nil {
		t.Errorf("read_only alone: %+v; want Docker's default capabilities left alone", s)
	}
	h.deploy(withSecurity(app("api", "api:1.1", 1), spec.Security{Capabilities: &[]string{"CHOWN", "SETUID"}}))
	if s := specs()["replica"].Security; s == nil || !s.DropCapabilities || !reflect.DeepEqual(s.Capabilities, []string{"CHOWN", "SETUID"}) || s.ReadOnly {
		t.Errorf("two capabilities kept: %+v", s)
	}
}

func TestWithoutSecurityNoContainerIsLockedDown(t *testing.T) {
	h := newHarness(t)
	specs := createdSpecs(h)
	h.deploy(withHook(app("my-api", "my-api:1.0", 1), "node", "migrate.js"))
	for kind, s := range specs() {
		if s.Security != nil {
			t.Errorf("the %s container was given %+v, which nobody asked for", kind, s.Security)
		}
	}
}

func TestNonRootFailsTheDeploymentBeforeAnyContainerIsCreated(t *testing.T) {
	for name, tc := range map[string]struct {
		imageUser, user string
		says            string
	}{
		"an image that names no user":   {"", "", "refuses image api:1.1: it names no user"},
		"an image that names root":      {"0:0", "", "id 0 is root"},
		"an image with a user by name":  {"node", "", "/etc/passwd"},
		"a name in deploy.yaml as well": {"1000", "app", `refuses user "app"`},
	} {
		t.Run(name, func(t *testing.T) {
			h := newHarness(t)
			// What runs before it stays as it is.
			if d := h.deploy(app("api", "api:1.0", 1)); d.Status != api.StatusActive {
				t.Fatalf("first deployment: %s (%s)", d.Status, d.Error)
			}
			before := h.rt.Containers()
			created := 0
			h.rt.CreateHook = func(docker.ContainerSpec) error { created++; return nil }

			h.rt.SetImageUser("api:1.1", tc.imageUser)
			a := withSecurity(withHook(app("api", "api:1.1", 1), "node", "migrate.js"), spec.Security{NonRoot: true})
			a.User = tc.user
			d := h.deploy(a)
			if d.Status != api.StatusFailed {
				t.Fatalf("status = %s, want the deployment refused", d.Status)
			}
			if !strings.HasPrefix(d.Error, "security.non_root refuses ") || !strings.Contains(d.Error, tc.says) || !strings.Contains(d.Error, `set user in deploy.yaml to a numeric id the image can run as, e.g. user: "1000:1000"`) {
				t.Errorf("error = %q, want it to say %q and what to set", d.Error, tc.says)
			}
			if created != 0 {
				t.Errorf("%d containers were asked for before the refusal, the hook's among them", created)
			}
			if runs := h.runs("api"); len(runs) != 0 {
				t.Errorf("the hook ran: %+v", runs)
			}
			if after := h.rt.Containers(); !reflect.DeepEqual(after, before) {
				t.Errorf("the running version was touched:\nbefore %+v\nafter  %+v", before, after)
			}
		})
	}
}

func TestNonRootAcceptsANumericUserFromTheImageOrFromDeployYaml(t *testing.T) {
	for name, tc := range map[string]struct{ imageUser, user string }{
		"the image's":                     {"1000:1000", ""},
		"deploy.yaml's over a root image": {"", "1000"},
		"deploy.yaml's over a name":       {"node", "1000:1000"},
	} {
		h := newHarness(t)
		h.rt.SetImageUser("api:1.0", tc.imageUser)
		a := withSecurity(app("api", "api:1.0", 1), spec.Security{NonRoot: true})
		a.User = tc.user
		if d := h.deploy(a); d.Status != api.StatusActive {
			t.Errorf("%s: %s (%s)", name, d.Status, d.Error)
		}
	}
}

// A tag can come to name another image between two pulls. The deployment was
// checked when it was made; every container made from it later is checked
// when it is made.
func TestNonRootIsHeldForContainersMadeAfterTheDeployment(t *testing.T) {
	ctx := context.Background()
	h := newHarness(t)
	h.rt.SetImageUser("api:1.0", "1000")
	if d := h.deploy(withSecurity(app("api", "api:1.0", 1), spec.Security{NonRoot: true})); d.Status != api.StatusActive {
		t.Fatalf("deployment: %s (%s)", d.Status, d.Error)
	}

	h.rt.SetImageUser("api:1.0", "")
	if _, err := h.engine.RunCommand(ctx, "api", []string{"id"}); err == nil {
		t.Error("RunCommand started a container from an image that now runs as root")
	}
	h.engine.Wait()
	runs := h.runs("api")
	if len(runs) != 1 || runs[0].Status != api.RunFailed {
		t.Fatalf("runs = %+v, want one that failed", runs)
	}
	if detail, err := h.engine.JobRun(ctx, "api", runs[0].ID); err != nil || !strings.Contains(detail.Output, "security.non_root refuses image api:1.0") {
		t.Errorf("the run says %q (%v), want it to name security.non_root", detail.Output, err)
	}
	if left := h.rt.JobContainers(); len(left) != 0 {
		t.Errorf("a container was created all the same: %+v", left)
	}
}
