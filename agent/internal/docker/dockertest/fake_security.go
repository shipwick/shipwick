package dockertest

import (
	"context"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// SetImageUser says which user an image's configuration names. An image
// nobody said anything about names none, like most images: it runs as root.
func (f *Fake) SetImageUser(image, user string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.imageUsers == nil {
		f.imageUsers = map[string]string{}
	}
	f.imageUsers[image] = user
}

// CheckNonRoot decides as the daemon's runtime does, by the same rule.
func (f *Fake) CheckNonRoot(_ context.Context, image, user string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	return docker.NonRoot(image, user, f.imageUsers[image])
}

// refuseRoot is what CreateContainer asks first, as the runtime's does.
func (f *Fake) refuseRoot(spec docker.ContainerSpec) error {
	if spec.Security == nil || !spec.Security.NonRoot {
		return nil
	}
	return f.CheckNonRoot(context.Background(), spec.Image, spec.User)
}
