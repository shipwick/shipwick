package docker

import (
	"context"
	"fmt"

	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	"github.com/shipwick/shipwick/pkg/spec"
)

// Security is what a container goes without on top of what every container
// goes without (see CreateContainer). Nothing in it gives a container
// anything: the zero value is a container as it always was.
type Security struct {
	// ReadOnly mounts the root filesystem read-only. Volumes and Tmpfs stay
	// writable.
	ReadOnly bool
	// Tmpfs are directories in memory, each bounded by its size.
	Tmpfs []Tmpfs
	// DropCapabilities takes every capability away except Capabilities,
	// which are names out of the daemon's default set.
	DropCapabilities bool
	Capabilities     []string
	// NonRoot refuses a container whose process would be root.
	NonRoot bool
}

// Tmpfs is one directory in memory.
type Tmpfs struct {
	Path      string
	SizeBytes int64
}

// configureSecurity applies the security block. The daemon's seccomp and
// AppArmor profiles are not touched: a container has the defaults, as before.
func configureSecurity(spec ContainerSpec, host *container.HostConfig) {
	s := spec.Security
	if s == nil {
		return
	}
	host.ReadonlyRootfs = s.ReadOnly
	for _, t := range s.Tmpfs {
		// Mounted by the daemon with nosuid, nodev and noexec, which is what
		// scratch space should have, and world-writable with the sticky bit
		// like /tmp: the container's user is not known here.
		host.Mounts = append(host.Mounts, mount.Mount{
			Type:         mount.TypeTmpfs,
			Target:       t.Path,
			TmpfsOptions: &mount.TmpfsOptions{SizeBytes: t.SizeBytes, Mode: 0o1777},
		})
	}
	if s.DropCapabilities {
		// All of them go and the kept ones come back: the result is a subset
		// of the default set whatever the daemon's default is.
		host.CapDrop = []string{"ALL"}
		host.CapAdd = append([]string(nil), s.Capabilities...)
	}
}

// RootError says that a container was refused because it would run as root,
// or as a user that cannot be shown not to be root.
type RootError struct {
	Image string
	// User is what the container would have been started as; FromImage says
	// that it is the image's own, deploy.yaml naming none.
	User      string
	FromImage bool
	// Why is spec.RootUser's verdict on User.
	Why string
}

func (e *RootError) Error() string {
	const remedy = `set user in deploy.yaml to a numeric id the image can run as, e.g. user: "1000:1000"`
	switch {
	case !e.FromImage:
		return fmt.Sprintf("security.non_root refuses user %q: %s; %s", e.User, e.Why, remedy)
	case e.User == "":
		return fmt.Sprintf("security.non_root refuses image %s: it names no user, and a container without one runs as root; %s, or build the image with a USER instruction", e.Image, remedy)
	}
	return fmt.Sprintf("security.non_root refuses image %s, which runs as user %q: %s; %s", e.Image, e.User, e.Why, remedy)
}

// NonRoot decides whether a container of image started as user — deploy.yaml's,
// empty for none — runs as someone other than root, given the user the
// image's configuration names. It returns a *RootError when that cannot be
// held to be so.
func NonRoot(image, user, imageUser string) error {
	effective, fromImage := user, false
	if effective == "" {
		effective, fromImage = imageUser, true
	}
	if why := spec.RootUser(effective); why != "" {
		return &RootError{Image: image, User: effective, FromImage: fromImage, Why: why}
	}
	return nil
}

// CheckNonRoot is NonRoot for an image the daemon has: its configuration is
// where the image's user is written. An image that is not there wraps
// ErrNotFound.
func (r *Runtime) CheckNonRoot(ctx context.Context, image, user string) error {
	res, err := r.cli.ImageInspect(ctx, image)
	if err != nil {
		return fmt.Errorf("inspect image %s: %w", image, wrapNotFound(err))
	}
	imageUser := ""
	if res.Config != nil {
		imageUser = res.Config.User
	}
	return NonRoot(image, user, imageUser)
}
