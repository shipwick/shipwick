package dockertest

import (
	"context"
	"fmt"
	"sync"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// registryState is the fake's registries. An image listed in private is only
// pulled with the credential it names; a login succeeds unless its registry
// is in refuse.
type registryState struct {
	mu      sync.Mutex
	private map[string]docker.RegistryAuth
	refuse  map[string]*docker.LoginError
	pulls   []Pull
	logins  []docker.RegistryAuth
}

// Pull is one call of PullImage: the image, and the credential it came with,
// nil when the runtime was left to the Docker configuration file.
type Pull struct {
	Image string
	Auth  *docker.RegistryAuth
}

func (f *Fake) registry() *registryState {
	r := &f.registries
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.private == nil {
		r.private, r.refuse = map[string]docker.RegistryAuth{}, map[string]*docker.LoginError{}
	}
	return r
}

// RequireAuth makes image private: pulling it is denied unless the pull
// carries exactly this credential.
func (f *Fake) RequireAuth(image string, auth docker.RegistryAuth) {
	r := f.registry()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.private[image] = auth
}

// RefuseLogin makes logins to registry fail with err.
func (f *Fake) RefuseLogin(registry string, err *docker.LoginError) {
	r := f.registry()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.refuse[registry] = err
}

// Pulls returns every pull attempted so far, denied ones included.
func (f *Fake) Pulls() []Pull {
	r := f.registry()
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]Pull(nil), r.pulls...)
}

// Logins returns the credentials RegistryLogin was asked to check.
func (f *Fake) Logins() []docker.RegistryAuth {
	r := f.registry()
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]docker.RegistryAuth(nil), r.logins...)
}

func (f *Fake) recordPull(image string, auth *docker.RegistryAuth) error {
	r := f.registry()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.pulls = append(r.pulls, Pull{Image: image, Auth: auth})
	if want, private := r.private[image]; private && (auth == nil || *auth != want) {
		return fmt.Errorf("pull %s: %w", image, docker.ErrPullDenied)
	}
	return nil
}

func (f *Fake) RegistryLogin(_ context.Context, auth docker.RegistryAuth) error {
	r := f.registry()
	r.mu.Lock()
	defer r.mu.Unlock()
	r.logins = append(r.logins, auth)
	if err := r.refuse[auth.Registry]; err != nil {
		return err
	}
	return nil
}

// PruneImage forgets a local image whether or not a container uses it, the
// way an operator's `docker image rm -f` on the server does.
func (f *Fake) PruneImage(image string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.local, image)
}
