package deploy

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/api"
)

func (h *harness) login(registry, username, password string) {
	h.t.Helper()
	if _, err := h.engine.RegistryLogin(context.Background(), registry, username, password, time.Now()); err != nil {
		h.t.Fatalf("RegistryLogin %s: %v", registry, err)
	}
}

// missingImagesFailCreate makes the fake refuse to create a container from an
// image it does not have, like the daemon.
func (h *harness) missingImagesFailCreate() {
	h.rt.CreateHook = func(s docker.ContainerSpec) error {
		if exists, _ := h.rt.ImageExists(context.Background(), s.Image); !exists {
			return errors.New("No such image: " + s.Image)
		}
		return nil
	}
}

func TestPullCarriesTheCredentialStoredForTheImagesRegistry(t *testing.T) {
	h := newHarness(t)
	h.login("ghcr.io", "octocat", "ghp_token")
	h.login("docker.io", "company", "hub_token")
	h.login("registry.example.com:5000", "ci", "local_token")

	for _, tt := range []struct {
		image string
		want  *docker.RegistryAuth
	}{
		{"ghcr.io/company/api:1.0", &docker.RegistryAuth{Registry: "ghcr.io", Username: "octocat", Password: "ghp_token"}},
		// Docker Hub, however the image names it.
		{"nginx:1.27", &docker.RegistryAuth{Registry: "docker.io", Username: "company", Password: "hub_token"}},
		{"company/web:2", &docker.RegistryAuth{Registry: "docker.io", Username: "company", Password: "hub_token"}},
		{"index.docker.io/company/web:3", &docker.RegistryAuth{Registry: "docker.io", Username: "company", Password: "hub_token"}},
		{"registry.example.com:5000/team/job:4", &docker.RegistryAuth{Registry: "registry.example.com:5000", Username: "ci", Password: "local_token"}},
		// Another port is another registry, and so is one nobody logged in
		// to: the runtime is left to the Docker configuration file.
		{"registry.example.com/team/job:4", nil},
		{"quay.io/company/tool:5", nil},
	} {
		d := h.deploy(app("my-api", tt.image, 1))
		if d.Status != api.StatusActive {
			t.Fatalf("%s: %s %s", tt.image, d.Status, d.Error)
		}
		pulls := h.rt.Pulls()
		got := pulls[len(pulls)-1]
		if got.Image != tt.image {
			t.Fatalf("last pull was %s, want %s", got.Image, tt.image)
		}
		switch {
		case tt.want == nil && got.Auth != nil:
			t.Errorf("%s was pulled with the credential for %s; want none", tt.image, got.Auth.Registry)
		case tt.want != nil && (got.Auth == nil || *got.Auth != *tt.want):
			t.Errorf("%s was pulled with %+v, want %+v", tt.image, got.Auth, tt.want)
		}
	}
}

func TestAPullRefusedForAuthenticationFailsTheDeploymentAndNamesTheCommand(t *testing.T) {
	h := newHarness(t)
	const image = "ghcr.io/company/api:1.0"
	right := docker.RegistryAuth{Registry: "ghcr.io", Username: "octocat", Password: "ghp_token"}
	h.rt.RequireAuth(image, right)

	d := h.deploy(app("my-api", image, 1))
	if d.Status != api.StatusFailed || d.Error != "pull access denied for ghcr.io/company/api: run shipwick registry login ghcr.io (or check the image name)" {
		t.Fatalf("without a credential: %s %q", d.Status, d.Error)
	}

	// A stored credential that the registry does not accept for this image
	// is said to be that: logging in for the first time is not the advice.
	h.login("ghcr.io", "octocat", "expired")
	d = h.deploy(app("my-api", image, 1))
	if d.Status != api.StatusFailed || !strings.Contains(d.Error, "the credential stored for ghcr.io does not give access") || !strings.Contains(d.Error, "shipwick registry login ghcr.io") {
		t.Fatalf("with a wrong credential: %s %q", d.Status, d.Error)
	}
	for _, e := range h.events(d.ID) {
		if strings.Contains(e.Message, "expired") {
			t.Errorf("an event carries the password: %q", e.Message)
		}
	}

	h.login("ghcr.io", right.Username, right.Password)
	if d = h.deploy(app("my-api", image, 1)); d.Status != api.StatusActive {
		t.Fatalf("with the right credential: %s %q", d.Status, d.Error)
	}
}

func TestADeniedPullStillFallsBackToTheLocalCopy(t *testing.T) {
	h := newHarness(t)
	const image = "ghcr.io/company/api:1.0"
	h.rt.RequireAuth(image, docker.RegistryAuth{Registry: "ghcr.io", Username: "octocat", Password: "ghp_token"})
	h.rt.AddLocalImage(image)

	d := h.deploy(app("my-api", image, 1))
	if d.Status != api.StatusActive {
		t.Fatalf("%s %q", d.Status, d.Error)
	}
	var warned bool
	for _, e := range h.events(d.ID) {
		warned = warned || (e.Level == api.LevelWarn && strings.Contains(e.Message, "using the local copy") && strings.Contains(e.Message, "shipwick registry login ghcr.io"))
	}
	if !warned {
		t.Error("the fallback must be recorded with what to do about the refusal")
	}
}

func TestTheSupervisorPullsAPrunedImageAgainWithTheStoredCredential(t *testing.T) {
	s := newSupervised(t)
	const image = "ghcr.io/company/api:1.0"
	cred := docker.RegistryAuth{Registry: "ghcr.io", Username: "octocat", Password: "ghp_token"}
	s.rt.RequireAuth(image, cred)
	s.login("ghcr.io", cred.Username, cred.Password)
	s.missingImagesFailCreate()
	if d := s.deploy(app("my-api", image, 1)); d.Status != api.StatusActive {
		t.Fatalf("deploy: %s %q", d.Status, d.Error)
	}
	pullsBefore := len(s.rt.Pulls())

	// The container and its image disappear behind Shipwick's back.
	if err := s.rt.RemoveContainer(context.Background(), s.container(t, 1).ID); err != nil {
		t.Fatal(err)
	}
	s.rt.PruneImage(image)
	s.advance(time.Second)
	s.engine.Wait()

	pulls := s.rt.Pulls()
	if len(pulls) != pullsBefore+1 {
		t.Fatalf("pulls = %d, want one more than %d", len(pulls), pullsBefore)
	}
	if last := pulls[len(pulls)-1]; last.Image != image || last.Auth == nil || *last.Auth != cred {
		t.Errorf("the image was pulled again with %+v, want the stored credential", last.Auth)
	}
	if c := s.container(t, 1); !c.Running {
		t.Errorf("replica not recreated: %+v", c)
	}
}

func TestAOneOffCommandPullsAPrunedImageAgainWithTheStoredCredential(t *testing.T) {
	h := newHarness(t)
	const image = "ghcr.io/company/api:1.0"
	cred := docker.RegistryAuth{Registry: "ghcr.io", Username: "octocat", Password: "ghp_token"}
	h.rt.RequireAuth(image, cred)
	h.login("ghcr.io", cred.Username, cred.Password)
	h.missingImagesFailCreate()
	if d := h.deploy(app("my-api", image, 1)); d.Status != api.StatusActive {
		t.Fatalf("deploy: %s %q", d.Status, d.Error)
	}
	h.rt.PruneImage(image)

	if _, err := h.engine.RunCommand(context.Background(), "my-api", []string{"migrate"}); err != nil {
		t.Fatalf("RunCommand: %v", err)
	}
	h.engine.Wait()
	pulls := h.rt.Pulls()
	if last := pulls[len(pulls)-1]; len(pulls) != 2 || last.Auth == nil || *last.Auth != cred {
		t.Errorf("pulls = %+v, want a second one with the stored credential", pulls)
	}
	if runs := h.runs("my-api"); len(runs) != 1 || runs[0].Status != api.RunSucceeded {
		t.Errorf("runs = %+v", runs)
	}

	// Without the credential the run fails with the sentence a deployment
	// would fail with.
	if err := h.store.DeleteRegistry(context.Background(), "ghcr.io"); err != nil {
		t.Fatal(err)
	}
	h.rt.PruneImage(image)
	_, err := h.engine.RunCommand(context.Background(), "my-api", []string{"migrate"})
	if err == nil || !strings.Contains(err.Error(), "run shipwick registry login ghcr.io") {
		t.Errorf("RunCommand without the credential = %v", err)
	}
}

func TestRegistryLoginChecksTheCredentialBeforeItStoresIt(t *testing.T) {
	h := newHarness(t)
	ctx := context.Background()
	h.rt.RefuseLogin("ghcr.io", &docker.LoginError{Refused: true, Message: "denied: denied"})
	h.rt.RefuseLogin("registry.example.com", &docker.LoginError{Message: "dial tcp: lookup registry.example.com: no such host"})

	_, err := h.engine.RegistryLogin(ctx, "ghcr.io", "octocat", "typo", time.Now())
	var refused *RegistryLoginError
	if !errors.As(err, &refused) || !refused.Refused || refused.Error() != "ghcr.io refused the login: denied: denied" {
		t.Fatalf("refused login = %v", err)
	}
	_, err = h.engine.RegistryLogin(ctx, "registry.example.com", "ci", "token", time.Now())
	if !errors.As(err, &refused) || refused.Refused || !strings.HasPrefix(refused.Error(), "could not log in to registry.example.com: ") {
		t.Fatalf("unreachable registry = %v", err)
	}
	if list, _ := h.store.ListRegistries(ctx); len(list) != 0 {
		t.Errorf("a credential that was not accepted was stored: %+v", list)
	}

	// Docker Hub under any of its names is one registry, and it is asked
	// under the one image references use.
	name, err := h.engine.RegistryLogin(ctx, "Index.Docker.IO", "company", "hub_token", time.Now())
	if err != nil || name != "docker.io" {
		t.Fatalf("RegistryLogin = %q, %v", name, err)
	}
	logins := h.rt.Logins()
	if last := logins[len(logins)-1]; last != (docker.RegistryAuth{Registry: "docker.io", Username: "company", Password: "hub_token"}) {
		t.Errorf("the registry was asked about %+v", last)
	}
	if _, _, found, _ := h.store.RegistryCredential(ctx, "docker.io"); !found {
		t.Error("the accepted credential was not stored")
	}

	// Refused before the registry is asked.
	before := len(h.rt.Logins())
	for _, tt := range []struct{ registry, username, password string }{
		{"https://ghcr.io", "u", "p"},
		{"ghcr.io/company", "u", "p"},
		{"", "u", "p"},
		{"ghcr.io", "", "p"},
		{"ghcr.io", "u", ""},
		{"ghcr.io", "a:b", "p"},
	} {
		if _, err := h.engine.RegistryLogin(ctx, tt.registry, tt.username, tt.password, time.Now()); err == nil {
			t.Errorf("RegistryLogin(%q, %q, %q) was accepted", tt.registry, tt.username, tt.password)
		}
	}
	if len(h.rt.Logins()) != before {
		t.Error("an invalid request reached the registry")
	}
}
