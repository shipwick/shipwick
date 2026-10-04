package deploy

import (
	"context"
	"errors"

	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/store"
)

// containerFor is what every container made from a deployment's spec starts
// from: a replica, the pre-deploy hook, a job, `shipwick run`, the container
// of backups.before, the one a backup is verified in. Each adds what makes
// it what it is — a replica index and its ports, a job's name and command.
// What the application runs as and what its containers go without are set
// here and nowhere else, so that a one-off container cannot be the way around
// the security block of deploy.yaml.
func containerFor(d store.Deployment) docker.ContainerSpec {
	cspec := docker.ContainerSpec{
		App:          d.Application,
		DeploymentID: d.ID,
		Sequence:     d.Sequence,
		Image:        d.Spec.Image,
		Env:          d.Spec.Env,
		NanoCPUs:     d.Spec.Resources.NanoCPUs(),
		MemoryBytes:  d.Spec.Resources.MemoryBytes,
		User:         d.Spec.User,
		Init:         d.Spec.Init,
	}
	if s := d.Spec.Security; s != nil {
		sec := &docker.Security{ReadOnly: s.ReadOnly, NonRoot: s.NonRoot}
		for _, t := range s.Tmpfs {
			sec.Tmpfs = append(sec.Tmpfs, docker.Tmpfs{Path: t.Path, SizeBytes: t.SizeBytes})
		}
		if s.Capabilities != nil {
			sec.DropCapabilities, sec.Capabilities = true, *s.Capabilities
		}
		cspec.Security = sec
	}
	return cspec
}

// nonRootChecker is what security.non_root needs of the runtime beyond the
// Runtime interface: the user an image names is in the image's configuration.
type nonRootChecker interface {
	CheckNonRoot(ctx context.Context, image, user string) error
}

// refuseRoot fails a deployment whose containers would run as root when its
// spec says they must not, once the image is on the server and before
// anything is made from it. The runtime refuses each such container again
// when it is created: an image can change under a tag between two pulls.
func (e *Engine) refuseRoot(ctx context.Context, d *store.Deployment) error {
	if d.Spec.Security == nil || !d.Spec.Security.NonRoot {
		return nil
	}
	checker, ok := e.rt.(nonRootChecker)
	if !ok {
		return errors.New("security.non_root cannot be checked: the container runtime does not say which user an image runs as")
	}
	return checker.CheckNonRoot(ctx, d.Spec.Image, d.Spec.User)
}
