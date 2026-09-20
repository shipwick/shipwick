// Package docker is a thin layer over the Docker Engine API. It exposes only
// the operations Shipwick needs, in Shipwick's vocabulary, and never shells out
// to the docker CLI.
package docker

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"sort"
	"strconv"
	"strings"
	"time"

	cerrdefs "github.com/containerd/errdefs"
	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/network"
	"github.com/moby/moby/client"
)

// ErrNotFound is returned when a container or image does not exist.
var ErrNotFound = errors.New("not found")

// ContainerSpec describes one replica container to create.
type ContainerSpec struct {
	App          string
	DeploymentID int64
	Sequence     int
	Replica      int
	Image        string
	Env          map[string]string
	NanoCPUs     int64 // 0 = unlimited
	MemoryBytes  int64 // 0 = unlimited
}

// Container is the runtime state of a Shipwick-managed container.
type Container struct {
	ID           string
	Name         string
	App          string
	DeploymentID int64
	Replica      int
	Image        string
	State        string // created, running, paused, restarting, removing, exited, dead
	Running      bool
	IP           string // address on the Shipwick network; empty unless running

	// The fields below are only populated by InspectContainer.
	ExitCode  int
	OOMKilled bool
	StartedAt *time.Time
	// OnServicesNetwork is false for a replica created before the services
	// network existed. ServiceNames are the names it answers to there.
	OnServicesNetwork bool
	ServiceNames      []string
}

type LogEntry struct {
	Stream  string // stdout | stderr
	Time    time.Time
	Message string
}

type Info struct {
	Hostname      string
	OS            string
	Kernel        string
	Architecture  string
	DockerVersion string
	CPUs          int
	MemoryBytes   int64
}

type Runtime struct {
	cli        *client.Client
	network    string
	services   string
	configPath string
}

// New connects to the Docker daemon selected by the standard environment
// variables (DOCKER_HOST, ...), defaulting to the local socket.
func New(network string) (*Runtime, error) {
	cli, err := client.New(client.FromEnv)
	if err != nil {
		return nil, fmt.Errorf("create docker client: %w", err)
	}
	return &Runtime{cli: cli, network: network, services: ServicesNetwork(network), configPath: dockerConfigPath()}, nil
}

// ServicesNetwork names the second network of an installation. Every replica
// is on it from birth, but carries its application's names there only while it
// is ready: those names are how the reverse proxy and other applications find
// it. They cannot live on the main network, because Docker sets an endpoint's
// aliases when it is connected and never afterwards — to gain or lose one, a
// replica would have to leave the network that holds its database connections.
func ServicesNetwork(network string) string { return network + "-services" }

func (r *Runtime) Close() error {
	return r.cli.Close()
}

func (r *Runtime) Ping(ctx context.Context) error {
	if _, err := r.cli.Ping(ctx, client.PingOptions{NegotiateAPIVersion: true}); err != nil {
		return fmt.Errorf("docker daemon unreachable: %w", err)
	}
	return nil
}

func (r *Runtime) Info(ctx context.Context) (Info, error) {
	res, err := r.cli.Info(ctx, client.InfoOptions{})
	if err != nil {
		return Info{}, fmt.Errorf("docker info: %w", err)
	}
	return Info{
		Hostname:      res.Info.Name,
		OS:            res.Info.OperatingSystem,
		Kernel:        res.Info.KernelVersion,
		Architecture:  res.Info.Architecture,
		DockerVersion: res.Info.ServerVersion,
		CPUs:          res.Info.NCPU,
		MemoryBytes:   res.Info.MemTotal,
	}, nil
}

// EnsureNetwork creates the two bridge networks of an installation, if they do
// not exist yet.
func (r *Runtime) EnsureNetwork(ctx context.Context) error {
	for _, name := range []string{r.network, r.services} {
		if err := r.ensureNetwork(ctx, name); err != nil {
			return err
		}
	}
	return nil
}

func (r *Runtime) ensureNetwork(ctx context.Context, name string) error {
	_, err := r.cli.NetworkInspect(ctx, name, client.NetworkInspectOptions{})
	if err == nil {
		return nil
	}
	if !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("inspect network %s: %w", name, err)
	}
	_, err = r.cli.NetworkCreate(ctx, name, client.NetworkCreateOptions{
		Driver: "bridge",
		Labels: map[string]string{LabelManaged: "true"},
	})
	// Lost a creation race: someone else made it, which is just as good.
	if err != nil && !cerrdefs.IsConflict(err) && !cerrdefs.IsAlreadyExists(err) {
		return fmt.Errorf("create network %s: %w", name, err)
	}
	return nil
}

// AttachSelf connects the agent's own container to the Shipwick network, so
// that health checks can reach application containers by their address. It
// reports false, without error, when the agent is not running in a container
// this daemon knows — a host process, which on Linux reaches bridge networks
// directly.
func (r *Runtime) AttachSelf(ctx context.Context) (bool, error) {
	// Inside a container the hostname is, unless overridden, the short
	// container ID.
	hostname, err := os.Hostname()
	if err != nil || len(hostname) < 12 {
		return false, nil
	}
	res, err := r.cli.ContainerInspect(ctx, hostname, client.ContainerInspectOptions{})
	if cerrdefs.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("inspect own container: %w", err)
	}
	self := res.Container
	// A name lookup can match by coincidence; an ID prefix cannot.
	if !strings.HasPrefix(self.ID, hostname) {
		return false, nil
	}
	if self.NetworkSettings != nil && self.NetworkSettings.Networks[r.network] != nil {
		return true, nil
	}
	_, err = r.cli.NetworkConnect(ctx, r.network, client.NetworkConnectOptions{Container: self.ID})
	if err != nil {
		return false, fmt.Errorf("connect agent container to network %s: %w", r.network, err)
	}
	return true, nil
}

// PullImage pulls image, authenticating with credentials from the Docker CLI
// config when the registry has an entry there.
func (r *Runtime) PullImage(ctx context.Context, image string) error {
	auth, err := registryAuth(r.configPath, image)
	if err != nil {
		return err
	}
	resp, err := r.cli.ImagePull(ctx, image, client.ImagePullOptions{RegistryAuth: auth})
	if err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	defer resp.Close()
	if err := resp.Wait(ctx); err != nil {
		return fmt.Errorf("pull %s: %w", image, err)
	}
	return nil
}

func (r *Runtime) ImageExists(ctx context.Context, image string) (bool, error) {
	_, err := r.cli.ImageInspect(ctx, image)
	if cerrdefs.IsNotFound(err) {
		return false, nil
	} else if err != nil {
		return false, fmt.Errorf("inspect image %s: %w", image, err)
	}
	return true, nil
}

// CreateContainer creates (but does not start) a replica container attached
// to the Shipwick network. Containers publish no host ports: the reverse proxy
// reaches them over the network.
func (r *Runtime) CreateContainer(ctx context.Context, spec ContainerSpec) (id, name string, err error) {
	env := make([]string, 0, len(spec.Env))
	for k, v := range spec.Env {
		env = append(env, k+"="+v)
	}
	sort.Strings(env)

	name = ContainerName(spec.App, spec.Sequence, spec.Replica)
	res, err := r.cli.ContainerCreate(ctx, client.ContainerCreateOptions{
		Name: name,
		Config: &container.Config{
			Image:  spec.Image,
			Env:    env,
			Labels: containerLabels(spec.App, spec.DeploymentID, spec.Replica),
		},
		HostConfig: &container.HostConfig{
			NetworkMode: container.NetworkMode(r.network),
			// Restarts are owned by Shipwick's supervisor, which applies
			// backoff and crash-loop detection; Docker must not compete.
			RestartPolicy: container.RestartPolicy{Name: container.RestartPolicyDisabled},
			Resources: container.Resources{
				NanoCPUs: spec.NanoCPUs,
				Memory:   spec.MemoryBytes,
				// Equal to Memory: the limit is a hard cap, swap does not extend it.
				MemorySwap: spec.MemoryBytes,
			},
			Privileged:  false,
			SecurityOpt: []string{"no-new-privileges:true"},
			// Bounded logs: a chatty app must not fill the server's disk.
			LogConfig: container.LogConfig{
				Type:   "json-file",
				Config: map[string]string{"max-size": "10m", "max-file": "3"},
			},
		},
	})
	if err != nil {
		return "", "", fmt.Errorf("create container %s: %w", name, err)
	}
	// On the services network from birth, nameless: the replica can find other
	// applications while it starts, and nobody can find it until it is ready.
	if err := r.connectServices(ctx, res.ID, nil); err != nil {
		_ = r.RemoveContainer(context.WithoutCancel(ctx), res.ID)
		return "", "", fmt.Errorf("create container %s: %w", name, err)
	}
	return res.ID, name, nil
}

// SetServiceNames makes names the DNS names of the container on the services
// network — all of them, replacing any it had; none takes it out of service
// discovery without touching its other connections.
//
// Docker cannot change the aliases of a connected endpoint, so this leaves the
// services network and joins it again. Whatever the container had open over
// that network is cut; what it holds over the main network — its database, the
// agent's health probes — is not.
func (r *Runtime) SetServiceNames(ctx context.Context, id string, names []string) error {
	_, err := r.cli.NetworkDisconnect(ctx, r.services, client.NetworkDisconnectOptions{Container: id, Force: true})
	// Not connected is where an installation that predates the services
	// network starts from, and where a failed earlier attempt leaves things.
	if err != nil && !cerrdefs.IsNotFound(err) && !isNotConnected(err) {
		return fmt.Errorf("leave network %s: %w", r.services, wrapNotFound(err))
	}
	return r.connectServices(ctx, id, names)
}

func (r *Runtime) connectServices(ctx context.Context, id string, names []string) error {
	_, err := r.cli.NetworkConnect(ctx, r.services, client.NetworkConnectOptions{
		Container:      id,
		EndpointConfig: &network.EndpointSettings{Aliases: names},
	})
	if err != nil {
		return fmt.Errorf("join network %s: %w", r.services, wrapNotFound(err))
	}
	return nil
}

// isNotConnected recognizes Docker's answer to disconnecting a container from
// a network it is not on, which has no error type of its own.
func isNotConnected(err error) bool {
	return strings.Contains(err.Error(), "is not connected to")
}

func (r *Runtime) StartContainer(ctx context.Context, id string) error {
	if _, err := r.cli.ContainerStart(ctx, id, client.ContainerStartOptions{}); err != nil {
		return fmt.Errorf("start container: %w", wrapNotFound(err))
	}
	return nil
}

// StopContainer stops a container gracefully (SIGTERM, then SIGKILL after
// timeout). Stopping a stopped or missing container is not an error.
func (r *Runtime) StopContainer(ctx context.Context, id string, timeout time.Duration) error {
	seconds := int(timeout.Seconds())
	_, err := r.cli.ContainerStop(ctx, id, client.ContainerStopOptions{Timeout: &seconds})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("stop container: %w", err)
	}
	return nil
}

// RemoveContainer force-removes a container. Removing a missing container is
// not an error, which keeps cleanup paths idempotent.
func (r *Runtime) RemoveContainer(ctx context.Context, id string) error {
	_, err := r.cli.ContainerRemove(ctx, id, client.ContainerRemoveOptions{Force: true, RemoveVolumes: true})
	if err != nil && !cerrdefs.IsNotFound(err) {
		return fmt.Errorf("remove container: %w", err)
	}
	return nil
}

func (r *Runtime) InspectContainer(ctx context.Context, id string) (Container, error) {
	res, err := r.cli.ContainerInspect(ctx, id, client.ContainerInspectOptions{})
	if err != nil {
		return Container{}, fmt.Errorf("inspect container: %w", wrapNotFound(err))
	}
	in := res.Container

	c := Container{ID: in.ID, Name: strings.TrimPrefix(in.Name, "/")}
	if in.Config != nil {
		c.Image = in.Config.Image
		c.applyLabels(in.Config.Labels)
	}
	if in.State != nil {
		c.State = string(in.State.Status)
		c.Running = in.State.Running
		c.ExitCode = in.State.ExitCode
		c.OOMKilled = in.State.OOMKilled
		if t, err := time.Parse(time.RFC3339Nano, in.State.StartedAt); err == nil && !t.IsZero() {
			t = t.UTC()
			c.StartedAt = &t
		}
	}
	if in.NetworkSettings != nil {
		if ep := in.NetworkSettings.Networks[r.network]; ep != nil && ep.IPAddress.IsValid() {
			c.IP = ep.IPAddress.String()
		}
		if ep := in.NetworkSettings.Networks[r.services]; ep != nil {
			c.OnServicesNetwork = true
			for _, alias := range ep.Aliases {
				// Older daemons list the container's own short ID among the
				// aliases; it is not a name anybody gave it.
				if !strings.HasPrefix(in.ID, alias) {
					c.ServiceNames = append(c.ServiceNames, alias)
				}
			}
			sort.Strings(c.ServiceNames)
		}
	}
	return c, nil
}

// ListContainers returns the Shipwick-managed containers of app, in any state.
// An empty app lists the containers of every application.
func (r *Runtime) ListContainers(ctx context.Context, app string) ([]Container, error) {
	filters := client.Filters{}
	filters.Add("label", LabelManaged+"=true")
	if app != "" {
		filters.Add("label", LabelApp+"="+app)
	}
	res, err := r.cli.ContainerList(ctx, client.ContainerListOptions{All: true, Filters: filters})
	if err != nil {
		return nil, fmt.Errorf("list containers: %w", err)
	}

	out := make([]Container, 0, len(res.Items))
	for _, item := range res.Items {
		c := Container{
			ID:      item.ID,
			Image:   item.Image,
			State:   string(item.State),
			Running: item.State == container.StateRunning,
		}
		if len(item.Names) > 0 {
			c.Name = strings.TrimPrefix(item.Names[0], "/")
		}
		c.applyLabels(item.Labels)
		if item.NetworkSettings != nil {
			if ep := item.NetworkSettings.Networks[r.network]; ep != nil && ep.IPAddress.IsValid() {
				c.IP = ep.IPAddress.String()
			}
		}
		out = append(out, c)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].DeploymentID != out[j].DeploymentID {
			return out[i].DeploymentID < out[j].DeploymentID
		}
		return out[i].Replica < out[j].Replica
	})
	return out, nil
}

func (c *Container) applyLabels(labels map[string]string) {
	c.App = labels[LabelApp]
	c.DeploymentID, _ = strconv.ParseInt(labels[LabelDeployment], 10, 64)
	c.Replica, _ = strconv.Atoi(labels[LabelReplica])
}

// Logs returns the last `tail` log lines of a container, oldest first.
func (r *Runtime) Logs(ctx context.Context, id string, tail int) ([]LogEntry, error) {
	rc, err := r.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Tail:       strconv.Itoa(tail),
	})
	if err != nil {
		return nil, fmt.Errorf("read logs: %w", wrapNotFound(err))
	}
	defer rc.Close()

	// Containers run without a TTY, so stdout and stderr arrive multiplexed.
	var stdout, stderr bytes.Buffer
	if _, err := stdcopy.StdCopy(&stdout, &stderr, rc); err != nil {
		return nil, fmt.Errorf("read logs: %w", err)
	}

	entries := append(parseLogLines("stdout", &stdout), parseLogLines("stderr", &stderr)...)
	sort.SliceStable(entries, func(i, j int) bool { return entries[i].Time.Before(entries[j].Time) })
	if len(entries) > tail {
		entries = entries[len(entries)-tail:]
	}
	return entries, nil
}

// FollowLogs streams a container's log, starting with its last `tail` lines,
// and calls emit for every line until the container's log ends or ctx is
// cancelled. Cancellation is the normal way to stop and is not an error.
func (r *Runtime) FollowLogs(ctx context.Context, id string, tail int, emit func(LogEntry)) error {
	rc, err := r.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		ShowStderr: true,
		Timestamps: true,
		Follow:     true,
		Tail:       strconv.Itoa(tail),
	})
	if err != nil {
		return fmt.Errorf("follow logs: %w", wrapNotFound(err))
	}
	defer rc.Close()

	stdout := &lineWriter{stream: "stdout", emit: emit}
	stderr := &lineWriter{stream: "stderr", emit: emit}
	_, err = stdcopy.StdCopy(stdout, stderr, rc)
	stdout.flush()
	stderr.flush()
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("follow logs: %w", err)
	}
	return nil
}

// lineWriter reassembles a byte stream into lines and emits each as a LogEntry.
type lineWriter struct {
	stream string
	emit   func(LogEntry)
	buf    []byte
}

// maxLogLineBytes bounds memory if a container writes without ever ending a line.
const maxLogLineBytes = 1024 * 1024

func (w *lineWriter) Write(p []byte) (int, error) {
	w.buf = append(w.buf, p...)
	for {
		i := bytes.IndexByte(w.buf, '\n')
		if i < 0 {
			break
		}
		w.emit(parseLogLine(w.stream, strings.TrimSuffix(string(w.buf[:i]), "\r")))
		w.buf = w.buf[i+1:]
	}
	if len(w.buf) > maxLogLineBytes {
		w.flush()
	}
	return len(p), nil
}

func (w *lineWriter) flush() {
	if len(w.buf) > 0 {
		w.emit(parseLogLine(w.stream, string(w.buf)))
		w.buf = nil
	}
}

func parseLogLines(stream string, buf *bytes.Buffer) []LogEntry {
	var out []LogEntry
	sc := bufio.NewScanner(buf)
	sc.Buffer(make([]byte, 0, 64*1024), maxLogLineBytes)
	for sc.Scan() {
		out = append(out, parseLogLine(stream, sc.Text()))
	}
	return out
}

// parseLogLine splits Docker's "<RFC3339Nano timestamp> <message>" format.
func parseLogLine(stream, line string) LogEntry {
	entry := LogEntry{Stream: stream, Message: line}
	if ts, msg, ok := strings.Cut(line, " "); ok {
		if t, err := time.Parse(time.RFC3339Nano, ts); err == nil {
			entry.Time, entry.Message = t.UTC(), msg
		}
	}
	return entry
}

func wrapNotFound(err error) error {
	if cerrdefs.IsNotFound(err) {
		return fmt.Errorf("%w: %v", ErrNotFound, err)
	}
	return err
}

// StatsSample is one reading of a container's resource counters. CPU usage is
// a rate, so it takes two samples to know it: see CPUPercent.
type StatsSample struct {
	Read        time.Time
	CPUTotal    uint64 // cumulative CPU time consumed by the container, ns
	SystemCPU   uint64 // cumulative CPU time of the whole host, ns
	OnlineCPUs  int
	MemoryBytes int64

	// Previous is the daemon's own earlier sample, present only when it was
	// asked for (which makes the call block for about a second).
	Previous *StatsSample
}

// Stats reads a container's resource usage. Without withPrevious it returns
// immediately with a single sample; with it, the daemon collects two samples
// a second apart, which is the only way to get a CPU rate from one call.
func (r *Runtime) Stats(ctx context.Context, id string, withPrevious bool) (StatsSample, error) {
	res, err := r.cli.ContainerStats(ctx, id, client.ContainerStatsOptions{IncludePreviousSample: withPrevious})
	if err != nil {
		return StatsSample{}, fmt.Errorf("read stats: %w", wrapNotFound(err))
	}
	defer res.Body.Close()

	var raw container.StatsResponse
	if err := json.NewDecoder(res.Body).Decode(&raw); err != nil {
		return StatsSample{}, fmt.Errorf("decode stats: %w", err)
	}
	sample := StatsSample{
		Read:        raw.Read,
		CPUTotal:    raw.CPUStats.CPUUsage.TotalUsage,
		SystemCPU:   raw.CPUStats.SystemUsage,
		OnlineCPUs:  onlineCPUs(raw.CPUStats),
		MemoryBytes: workingSet(raw.MemoryStats),
	}
	if withPrevious && !raw.PreRead.IsZero() {
		sample.Previous = &StatsSample{
			Read:       raw.PreRead,
			CPUTotal:   raw.PreCPUStats.CPUUsage.TotalUsage,
			SystemCPU:  raw.PreCPUStats.SystemUsage,
			OnlineCPUs: onlineCPUs(raw.PreCPUStats),
		}
	}
	return sample, nil
}

func onlineCPUs(s container.CPUStats) int {
	if s.OnlineCPUs > 0 {
		return int(s.OnlineCPUs)
	}
	return len(s.CPUUsage.PercpuUsage) // daemons on cgroup v1
}

// workingSet is memory the container actually holds on to: usage minus the
// page cache the kernel would give back under pressure. It is what
// `docker stats` shows, and what the memory limit is enforced against.
func workingSet(m container.MemoryStats) int64 {
	usage := m.Usage
	for _, key := range []string{"inactive_file", "total_inactive_file"} { // cgroup v2, v1
		if cache, ok := m.Stats[key]; ok {
			if cache < usage {
				usage -= cache
			}
			break
		}
	}
	return int64(usage)
}

// CPUPercent is the CPU used between two samples, in percent of one core:
// a container keeping two cores busy reports 200. It returns 0 when the
// samples cannot be compared (container restarted, counters went backwards).
func CPUPercent(prev, cur StatsSample) float64 {
	if cur.CPUTotal < prev.CPUTotal || cur.SystemCPU <= prev.SystemCPU || cur.OnlineCPUs == 0 {
		return 0
	}
	cpuDelta := float64(cur.CPUTotal - prev.CPUTotal)
	systemDelta := float64(cur.SystemCPU - prev.SystemCPU)
	return cpuDelta / systemDelta * float64(cur.OnlineCPUs) * 100
}
