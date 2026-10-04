package dockertest

import (
	"context"
	"fmt"
	"io"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// ErrDaemonDown is what a daemon that is not running answers through its
// socket, in the client's words.
var ErrDaemonDown = fmt.Errorf("%w: Cannot connect to the Docker daemon at unix:///var/run/docker.sock. Is the docker daemon running?", docker.ErrUnavailable)

// Outage is a Fake whose daemon can stop answering, in the two ways a real one
// does: Fail refuses every call at once, as a daemon that is not running does;
// Freeze accepts every call and answers none, as a daemon does that is paused,
// swapping or out of file descriptors. Restore ends either. The containers
// are the Fake's and are not touched by any of it: a daemon that is down has
// not stopped anything.
//
// Hand the Outage to the engine in place of the Fake it wraps.
type Outage struct {
	*Fake

	mu     sync.Mutex
	err    error
	frozen chan struct{} // closed by Restore
	held   chan struct{} // closed when the first call is held or refused
	asked  int
}

func NewOutage(f *Fake) *Outage {
	return &Outage{Fake: f, held: make(chan struct{})}
}

// Fail makes every call fail with err from now on.
func (o *Outage) Fail(err error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.err = err
}

// Freeze makes every call wait until Restore, or until its context ends.
func (o *Outage) Freeze() {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.frozen == nil {
		o.frozen = make(chan struct{})
	}
}

// Restore makes the daemon answer again; calls that were waiting go through.
func (o *Outage) Restore() {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.err = nil
	if o.frozen != nil {
		close(o.frozen)
		o.frozen = nil
	}
}

// Held is closed once a call has met the outage.
func (o *Outage) Held() <-chan struct{} { return o.held }

// Asked counts the calls that met the outage.
func (o *Outage) Asked() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.asked
}

// gate is what every call passes first.
func (o *Outage) gate(ctx context.Context) error {
	o.mu.Lock()
	err, frozen := o.err, o.frozen
	if err != nil || frozen != nil {
		if o.asked++; o.asked == 1 {
			close(o.held)
		}
	}
	o.mu.Unlock()
	if err != nil {
		return err
	}
	if frozen == nil {
		return nil
	}
	select {
	case <-frozen:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (o *Outage) EnsureNetwork(ctx context.Context) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.EnsureNetwork(ctx)
}

func (o *Outage) PullImage(ctx context.Context, image string, auth *docker.RegistryAuth) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.PullImage(ctx, image, auth)
}

func (o *Outage) ImageExists(ctx context.Context, image string) (bool, error) {
	if err := o.gate(ctx); err != nil {
		return false, err
	}
	return o.Fake.ImageExists(ctx, image)
}

func (o *Outage) RemoveImage(ctx context.Context, image string) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.RemoveImage(ctx, image)
}

func (o *Outage) CreateContainer(ctx context.Context, spec docker.ContainerSpec) (string, string, error) {
	if err := o.gate(ctx); err != nil {
		return "", "", err
	}
	return o.Fake.CreateContainer(ctx, spec)
}

func (o *Outage) StartContainer(ctx context.Context, id string) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.StartContainer(ctx, id)
}

func (o *Outage) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.StopContainer(ctx, id, timeout)
}

func (o *Outage) RemoveContainer(ctx context.Context, id string) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.RemoveContainer(ctx, id)
}

func (o *Outage) SetServiceNames(ctx context.Context, id string, names []string) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.SetServiceNames(ctx, id, names)
}

func (o *Outage) InspectContainer(ctx context.Context, id string) (docker.Container, error) {
	if err := o.gate(ctx); err != nil {
		return docker.Container{}, err
	}
	return o.Fake.InspectContainer(ctx, id)
}

func (o *Outage) ListContainers(ctx context.Context, app string) ([]docker.Container, error) {
	if err := o.gate(ctx); err != nil {
		return nil, err
	}
	return o.Fake.ListContainers(ctx, app)
}

func (o *Outage) Logs(ctx context.Context, id string, tail int) ([]docker.LogEntry, error) {
	if err := o.gate(ctx); err != nil {
		return nil, err
	}
	return o.Fake.Logs(ctx, id, tail)
}

func (o *Outage) FollowLogs(ctx context.Context, id string, tail int, emit func(docker.LogEntry)) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.FollowLogs(ctx, id, tail, emit)
}

func (o *Outage) ReadLogs(ctx context.Context, id string, w docker.LogWindow, emit func(docker.LogEntry)) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.ReadLogs(ctx, id, w, emit)
}

func (o *Outage) Stats(ctx context.Context, id string, withPrevious bool) (docker.StatsSample, error) {
	if err := o.gate(ctx); err != nil {
		return docker.StatsSample{}, err
	}
	return o.Fake.Stats(ctx, id, withPrevious)
}

func (o *Outage) Info(ctx context.Context) (docker.Info, error) {
	if err := o.gate(ctx); err != nil {
		return docker.Info{}, err
	}
	return o.Fake.Info(ctx)
}

func (o *Outage) Exec(ctx context.Context, id string, cmd []string, timeout time.Duration) (int, string, error) {
	if err := o.gate(ctx); err != nil {
		return 0, "", err
	}
	return o.Fake.Exec(ctx, id, cmd, timeout)
}

func (o *Outage) WaitContainer(ctx context.Context, id string) (int, error) {
	if err := o.gate(ctx); err != nil {
		return 0, err
	}
	return o.Fake.WaitContainer(ctx, id)
}

func (o *Outage) ExportPath(ctx context.Context, id, path string) (io.ReadCloser, error) {
	if err := o.gate(ctx); err != nil {
		return nil, err
	}
	return o.Fake.ExportPath(ctx, id, path)
}

func (o *Outage) ImportPath(ctx context.Context, id, path string, archive io.Reader) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.ImportPath(ctx, id, path, archive)
}

func (o *Outage) LoadImage(ctx context.Context, archive io.Reader) ([]string, error) {
	if err := o.gate(ctx); err != nil {
		return nil, err
	}
	return o.Fake.LoadImage(ctx, archive)
}

func (o *Outage) ListImages(ctx context.Context, repository string) ([]string, error) {
	if err := o.gate(ctx); err != nil {
		return nil, err
	}
	return o.Fake.ListImages(ctx, repository)
}

func (o *Outage) ListVolumes(ctx context.Context) ([]docker.Volume, error) {
	if err := o.gate(ctx); err != nil {
		return nil, err
	}
	return o.Fake.ListVolumes(ctx)
}

func (o *Outage) RemoveVolume(ctx context.Context, app, volume string) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.RemoveVolume(ctx, app, volume)
}

func (o *Outage) ProxyContainer(ctx context.Context) (string, error) {
	if err := o.gate(ctx); err != nil {
		return "", err
	}
	return o.Fake.ProxyContainer(ctx)
}

func (o *Outage) ImageLayers(ctx context.Context) ([][]string, error) {
	if err := o.gate(ctx); err != nil {
		return nil, err
	}
	return o.Fake.ImageLayers(ctx)
}

func (o *Outage) RegistryLogin(ctx context.Context, auth docker.RegistryAuth) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.RegistryLogin(ctx, auth)
}

func (o *Outage) FollowOutput(ctx context.Context, id string, since time.Time, emit func(line []byte)) error {
	if err := o.gate(ctx); err != nil {
		return err
	}
	return o.Fake.FollowOutput(ctx, id, since, emit)
}
