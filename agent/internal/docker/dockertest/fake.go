// Package dockertest provides an in-memory stand-in for the Docker runtime,
// used to test the deployment engine and the HTTP API without a Docker daemon.
package dockertest

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/shipwick/shipwick/agent/internal/docker"
)

// Fake implements the deployment engine's Runtime interface. Configure the
// exported fields before handing it to the engine; afterwards use the methods,
// which are safe for concurrent use.
type Fake struct {
	// PullErr, CreateErr and StartErr make the corresponding operation fail.
	PullErr   error
	CreateErr error
	StartErr  error
	// CrashImages lists images whose containers exit with code 1 right
	// after starting.
	CrashImages map[string]bool
	// CrashNames does the same for individual containers, by name
	// (e.g. "shipwick_web_2_2"): one bad replica among good ones.
	CrashNames map[string]bool
	// CreateHook, when set, can veto the creation of a container.
	CreateHook func(docker.ContainerSpec) error
	// CPUBusy is how many cores worth of CPU every running container burns,
	// and MemoryUsed how much memory it holds: what Stats reports.
	CPUBusy    float64
	MemoryUsed int64
	// PullDelay simulates a slow registry. It honors context cancellation.
	PullDelay time.Duration
	// StopDelay simulates a process that takes its time to exit on SIGTERM.
	StopDelay time.Duration
	// NamesErr makes SetServiceNames fail. NamesDelay is how long it takes,
	// which is how long a second caller for the same container has to collide.
	NamesErr   error
	NamesDelay time.Duration
	// Clock, when set, is the time Stats stamps its readings with, so tests
	// can space readings out without waiting.
	Clock func() time.Time
	// JobExits is the exit code a job container ends with, by job name;
	// unlisted jobs exit 0. Jobs finish the moment WaitContainer is called,
	// unless HoldJobs is set: then they run until ReleaseJob or Crash.
	JobExits map[string]int
	HoldJobs bool

	mu         sync.Mutex
	nextID     int
	containers map[string]*docker.Container
	specs      map[string]docker.ContainerSpec
	local      map[string]bool
	pulled     []string
	starts     map[string]int
	ips        map[string]string // stable per container, like a user-defined network
	peak       int               // most containers that ever existed at once

	statsCalls, blockingStatsCalls int
	nameChanges                    int
	renaming                       map[string]bool
	namedAt, stoppedAt             map[string]time.Time // by container name
	removedImages                  []string
	stopped                        map[string]chan struct{} // closed when the container stops or goes; see WaitContainer

	// ExecResults is what Exec answers, looked up by container name and then
	// by the command joined with spaces; a container matched by neither exits
	// 0 with no output. SetExecResult changes it while the engine runs.
	ExecResults map[string]ExecResult
	execCalls   []ExecCall
	// The fake's filesystem, see fake_archive.go: files by container, and
	// by Docker volume name for what lives under a mount point.
	files          map[string]map[string][]byte
	volumes        map[string]map[string][]byte
	removedVolumes []string
	// loaded are the references LoadImage was given, see fake_images.go.
	loaded []string
	// registries: private images and refused logins, see fake_registry.go.
	registries registryState

	// ProxyErr makes ProxyContainer fail: the proxy is not a container here.
	// The proxy's directories and what was removed from it: see fake_proxy.go.
	ProxyErr     error
	proxyDirs    map[string]bool
	proxyRemoved []string

	// layers are the images ImageLayers reports, see fake_layers.go.
	// LayersErr makes it fail; LoadErr makes LoadImage refuse an archive
	// after reading it, as the daemon does.
	layers    [][]string
	LayersErr error
	LoadErr   error
	// FollowErr makes FollowOutput fail. What containers print on standard
	// output and who followed it: see fake_traffic.go.
	FollowErr error
	output    map[string]chan []byte
	follows   []time.Time
	// How containers stop: see fake_stop.go.
	stop *stopControl
	// SaveErr makes SaveImage fail: see fake_export.go.
	SaveErr error
	// What containers print and when they stopped: see fake_logs.go.
	logs *logState
	// The user each image names: see fake_security.go.
	imageUsers map[string]string
	// namelessRemovals counts RemoveImage calls for no image at all: the
	// daemon has no route for one, and a socket proxy refuses it aloud.
	namelessRemovals int
	// Rootless and Unenforced are what Info says about the daemon: see
	// docker.Info.
	Rootless   bool
	Unenforced []string
}

// NamelessRemovals is how often an image without a name was to be removed.
func (f *Fake) NamelessRemovals() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.namelessRemovals
}

func New() *Fake {
	return &Fake{
		CrashImages: map[string]bool{},
		CrashNames:  map[string]bool{},
		containers:  map[string]*docker.Container{},
		specs:       map[string]docker.ContainerSpec{ProxyID: {}}, // the proxy's own files, see fake_proxy.go
		local:       map[string]bool{},
		starts:      map[string]int{},
		ips:         map[string]string{},
		renaming:    map[string]bool{},
		namedAt:     map[string]time.Time{},
		stoppedAt:   map[string]time.Time{},
		ExecResults: map[string]ExecResult{},
		files:       map[string]map[string][]byte{},
		volumes:     map[string]map[string][]byte{},
		JobExits:    map[string]int{},
		stopped:     map[string]chan struct{}{},
		proxyDirs:   map[string]bool{},
	}
}

// AddLocalImage marks an image as present in the local image store.
func (f *Fake) AddLocalImage(image string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.local[image] = true
}

// Crash simulates the container's process dying.
func (f *Fake) Crash(id string, exitCode int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[id]; ok {
		c.Running, c.State, c.ExitCode, c.IP = false, "exited", exitCode, ""
		f.finish(c)
		f.signalStopped(id)
	}
}

// Containers returns a snapshot of all containers, ordered like ListContainers.
func (f *Fake) Containers() []docker.Container {
	f.mu.Lock()
	defer f.mu.Unlock()
	out := make([]docker.Container, 0, len(f.containers))
	for _, c := range f.containers {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeploymentID != out[j].DeploymentID {
			return out[i].DeploymentID < out[j].DeploymentID
		}
		return out[i].Replica < out[j].Replica
	})
	return out
}

// Spec returns the spec a container was created with.
func (f *Fake) Spec(id string) docker.ContainerSpec {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.specs[id]
}

// Pulled returns the images pulled so far.
func (f *Fake) Pulled() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.pulled...)
}

func (f *Fake) EnsureNetwork(context.Context) error { return nil }

func (f *Fake) PullImage(ctx context.Context, image string, auth *docker.RegistryAuth) error {
	if err := f.recordPull(image, auth); err != nil {
		return err
	}
	if f.PullDelay > 0 {
		select {
		case <-time.After(f.PullDelay):
		case <-ctx.Done():
			return ctx.Err()
		}
	}
	if f.PullErr != nil {
		return f.PullErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	f.pulled = append(f.pulled, image)
	f.local[image] = true
	return nil
}

func (f *Fake) ImageExists(_ context.Context, image string) (bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.local[image], nil
}

func (f *Fake) CreateContainer(_ context.Context, spec docker.ContainerSpec) (string, string, error) {
	if f.CreateErr != nil {
		return "", "", f.CreateErr
	}
	if f.CreateHook != nil {
		if err := f.CreateHook(spec); err != nil {
			return "", "", err
		}
	}
	if err := f.refuseRoot(spec); err != nil {
		return "", "", err
	}
	f.mu.Lock()
	defer f.mu.Unlock()

	name, job := docker.ContainerName(spec.App, spec.Sequence, spec.Replica), ""
	if spec.Job != nil {
		name, job = docker.JobContainerName(spec.App, spec.Job.Name, spec.Job.RunID), spec.Job.Name
	}
	for _, c := range f.containers {
		if c.Name == name {
			return "", "", fmt.Errorf("container name %q is already in use", name)
		}
	}
	f.nextID++
	id := fmt.Sprintf("fake%04d", f.nextID)
	f.containers[id] = &docker.Container{
		ID:           id,
		Name:         name,
		App:          spec.App,
		DeploymentID: spec.DeploymentID,
		Replica:      spec.Replica,
		Job:          job,
		Image:        spec.Image,
		State:        "created",
		// Born on the services network, nameless.
		OnServicesNetwork: true,
	}
	f.specs[id] = spec
	if spec.Job != nil {
		f.containers[id].RunID = spec.Job.RunID
	}
	// Like the daemon: a volume exists once a container mounting it does.
	for _, m := range spec.Mounts {
		if name := docker.VolumeName(spec.App, m.Volume); f.volumes[name] == nil {
			f.volumes[name] = map[string][]byte{}
		}
	}
	f.ips[id] = fmt.Sprintf("172.18.0.%d", f.nextID+1)
	f.peak = max(f.peak, len(f.containers))
	return id, name, nil
}

func (f *Fake) StartContainer(_ context.Context, id string) error {
	if f.StartErr != nil {
		return f.StartErr
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return docker.ErrNotFound
	}
	// Like the daemon: a network namespace can only be joined while its
	// container runs.
	if beside := f.specs[id].Beside; beside != nil {
		if other, ok := f.containers[beside.Container]; !ok || !other.Running {
			return fmt.Errorf("cannot join network namespace of a non running container: %s", beside.Container)
		}
	}
	f.starts[id]++
	f.started(c)
	if f.CrashImages[c.Image] || f.CrashNames[c.Name] {
		c.Running, c.State, c.ExitCode = false, "exited", 1
		f.finish(c)
		return nil
	}
	now := time.Now().UTC()
	c.Running, c.State, c.ExitCode, c.StartedAt = true, "running", 0, &now
	c.OOMKilled = false
	c.IP = f.ips[id]
	return nil
}

func (f *Fake) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	time.Sleep(f.StopDelay)
	if err := f.holdStop(ctx, id); err != nil {
		return err
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[id]; ok && c.Running {
		c.Running, c.State, c.ExitCode, c.IP = false, "exited", f.stopExit(c, timeout), ""
		f.stoppedAt[c.Name] = time.Now()
		f.finish(c)
		f.signalStopped(id)
	}
	return nil
}

// Starts reports how many times StartContainer succeeded for a container,
// the initial start included.
func (f *Fake) Starts(id string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.starts[id]
}

func (f *Fake) RemoveContainer(_ context.Context, id string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.containers, id)
	delete(f.specs, id)
	delete(f.logState().lines, id)
	f.signalStopped(id)
	return nil
}

func (f *Fake) InspectContainer(_ context.Context, id string) (docker.Container, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return docker.Container{}, docker.ErrNotFound
	}
	return *c, nil
}

func (f *Fake) ListContainers(_ context.Context, app string) ([]docker.Container, error) {
	var out []docker.Container
	for _, c := range f.Containers() {
		if app == "" || c.App == app {
			out = append(out, c)
		}
	}
	return out, nil
}

func (f *Fake) Logs(_ context.Context, id string, tail int) ([]docker.LogEntry, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return nil, docker.ErrNotFound
	}
	if lines, ok := f.written(id, tail); ok {
		return lines, nil
	}
	return []docker.LogEntry{
		{Stream: "stdout", Time: time.Now().UTC(), Message: "log line from " + c.Name},
	}, nil
}

// FollowLogs emits the container's one log line, then, like a real follow,
// stays open until the caller cancels.
func (f *Fake) FollowLogs(ctx context.Context, id string, tail int, emit func(docker.LogEntry)) error {
	entries, err := f.Logs(ctx, id, tail)
	if err != nil {
		return err
	}
	if tail > 0 {
		for _, e := range entries {
			emit(e)
		}
	}
	<-ctx.Done()
	return nil
}

func (f *Fake) Info(context.Context) (docker.Info, error) {
	return docker.Info{
		Hostname:      "fake-host",
		OS:            "Fake Linux",
		Architecture:  "x86_64",
		DockerVersion: "0.0.0-fake",
		CPUs:          4,
		MemoryBytes:   8 << 30,
		Rootless:      f.Rootless,
		Unenforced:    f.Unenforced,
	}, nil
}

// PeakContainers reports the most containers that existed at the same time
// since the last ResetPeak.
func (f *Fake) PeakContainers() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.peak
}

func (f *Fake) ResetPeak() {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.peak = len(f.containers)
}

// Stats derives counters from the wall clock, the way real ones behave: they
// only ever grow, and the rate between two readings is CPUBusy cores.
func (f *Fake) Stats(_ context.Context, id string, withPrevious bool) (docker.StatsSample, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	c, ok := f.containers[id]
	if !ok {
		return docker.StatsSample{}, docker.ErrNotFound
	}
	if !c.Running {
		return docker.StatsSample{}, nil // Docker reports an empty reading for a stopped container
	}
	const cpus = 4
	at := func(t time.Time) docker.StatsSample {
		ns := uint64(t.UnixNano())
		return docker.StatsSample{
			Read:        t,
			SystemCPU:   ns * cpus,
			CPUTotal:    uint64(float64(ns) * f.CPUBusy),
			OnlineCPUs:  cpus,
			MemoryBytes: f.MemoryUsed,
		}
	}
	f.statsCalls++
	now := f.now()
	sample := at(now)
	if withPrevious {
		f.blockingStatsCalls++
		prev := at(now.Add(-time.Second))
		sample.Previous = &prev
	}
	return sample, nil
}

// StatsCalls reports how often Stats was called, and how many of those calls
// were the expensive kind that makes Docker wait for a second sample.
func (f *Fake) StatsCalls() (total, blocking int) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.statsCalls, f.blockingStatsCalls
}

// SetServiceNames replaces the names a container answers to on the services
// network, like the real runtime: any container, running or not.
func (f *Fake) SetServiceNames(_ context.Context, id string, names []string) error {
	f.mu.Lock()
	if f.NamesErr != nil {
		f.mu.Unlock()
		return f.NamesErr
	}
	if _, ok := f.containers[id]; !ok {
		f.mu.Unlock()
		return docker.ErrNotFound
	}
	// Leaving the network and joining it again are two calls to the daemon.
	// A second caller in between finds the endpoint already there.
	if f.renaming[id] {
		f.mu.Unlock()
		return fmt.Errorf("endpoint with name %s already exists in network services", id)
	}
	f.renaming[id] = true
	f.mu.Unlock()

	time.Sleep(f.NamesDelay)

	f.mu.Lock()
	defer f.mu.Unlock()
	delete(f.renaming, id)
	if c, ok := f.containers[id]; ok {
		c.OnServicesNetwork = true
		c.ServiceNames = append([]string(nil), names...)
		f.nameChanges++
		if len(names) > 0 {
			f.namedAt[c.Name] = time.Now()
		}
	}
	return nil
}

// Resolve answers like Docker's DNS on the services network: the names of the
// running containers that carry name, sorted. It is what the reverse proxy and
// other applications would reach.
func (f *Fake) Resolve(name string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.containers {
		if !c.Running {
			continue
		}
		for _, n := range c.ServiceNames {
			if n == name {
				out = append(out, c.Name)
			}
		}
	}
	sort.Strings(out)
	return out
}

// NameChanges counts SetServiceNames calls: each one cuts the container's
// connections over the services network, so tests watch that it stays rare.
func (f *Fake) NameChanges() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.nameChanges
}

// LeaveServicesNetwork turns a container into one created before the services
// network existed.
func (f *Fake) LeaveServicesNetwork(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if c, ok := f.containers[id]; ok {
		c.OnServicesNetwork = false
		c.ServiceNames = nil
	}
}

// NamedAt and StoppedAt report when a container, by name, was last given names
// and when it was stopped; the zero time if it never was.
func (f *Fake) NamedAt(name string) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.namedAt[name]
}

func (f *Fake) StoppedAt(name string) time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.stoppedAt[name]
}

// RemoveImage forgets a local image, unless a container — running or not —
// still uses it, like the real daemon without --force.
func (f *Fake) RemoveImage(_ context.Context, image string) error {
	f.mu.Lock()
	defer f.mu.Unlock()
	if image == "" {
		f.namelessRemovals++
		return docker.ErrImageNotFound
	}
	for _, c := range f.containers {
		if c.Image == image {
			return docker.ErrImageInUse
		}
	}
	if !f.local[image] {
		return docker.ErrImageNotFound
	}
	delete(f.local, image)
	f.removedImages = append(f.removedImages, image)
	return nil
}

// RemovedImages lists the images removed so far, in order.
func (f *Fake) RemovedImages() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]string(nil), f.removedImages...)
}
