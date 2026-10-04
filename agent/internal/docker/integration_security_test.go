//go:build integration

package docker

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"
	"time"
)

// nodeImage runs as root unless told otherwise and has a user with the id
// 1000: what an application that listens on a high port looks like.
const nodeImage = "node:22-alpine"

// securityRuntime is a runtime with the images of these tests pulled.
func securityRuntime(t *testing.T, images ...string) (rt *Runtime, ctx context.Context, app string) {
	t.Helper()
	rt, ctx = newIntegrationRuntime(t)
	if err := rt.EnsureNetwork(ctx); err != nil {
		t.Fatalf("EnsureNetwork: %v", err)
	}
	for _, image := range images {
		if err := rt.PullImage(ctx, image, nil); err != nil {
			t.Fatalf("PullImage: %v", err)
		}
	}
	return rt, ctx, fmt.Sprintf("it-%d", time.Now().UnixNano()%1_000_000)
}

// started creates and starts a container and removes it with the test.
func started(t *testing.T, ctx context.Context, rt *Runtime, spec ContainerSpec) string {
	t.Helper()
	id, _, err := rt.CreateContainer(ctx, spec)
	if err != nil {
		t.Fatalf("CreateContainer: %v", err)
	}
	t.Cleanup(func() { rt.RemoveContainer(context.WithoutCancel(ctx), id) })
	if err := rt.StartContainer(ctx, id); err != nil {
		t.Fatalf("StartContainer: %v", err)
	}
	return id
}

// answers waits until cmd succeeds inside the container.
func answers(t *testing.T, ctx context.Context, rt *Runtime, id string, cmd ...string) {
	t.Helper()
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		if code, _, err := rt.Exec(ctx, id, cmd, 5*time.Second); err == nil && code == 0 {
			return
		}
		if time.Now().After(deadline) {
			logs, _ := rt.Logs(ctx, id, 20)
			c, _ := rt.InspectContainer(ctx, id)
			t.Fatalf("%v never succeeded; running = %v, exit code %d, output %+v", cmd, c.Running, c.ExitCode, logs)
		}
	}
}

// effectiveCapabilities is the CapEff line of the container's first process.
func effectiveCapabilities(t *testing.T, ctx context.Context, rt *Runtime, id string) string {
	t.Helper()
	for _, line := range strings.Split(execIn(t, ctx, rt, id, "cat", "/proc/1/status"), "\n") {
		if value, ok := strings.CutPrefix(line, "CapEff:"); ok {
			return strings.TrimSpace(value)
		}
	}
	t.Fatal("no CapEff in /proc/1/status")
	return ""
}

// nginx as its image starts it — root that binds port 80, takes ownership of
// its cache directories and hands its workers to another user — under a
// read-only root, with the two directories it writes and the three
// capabilities that takes.
func TestIntegrationAReadOnlyRootIsReadOnlyAndTmpfsAndVolumesAreNot(t *testing.T) {
	rt, ctx, app := securityRuntime(t, testImage)
	t.Cleanup(func() { rt.RemoveVolume(context.WithoutCancel(ctx), app, "data") })
	id := started(t, ctx, rt, ContainerSpec{
		App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: testImage,
		Mounts: []Mount{{Volume: "data", Path: "/data"}},
		Security: &Security{
			ReadOnly:         true,
			Tmpfs:            []Tmpfs{{Path: "/var/cache/nginx", SizeBytes: 16 << 20}, {Path: "/var/run", SizeBytes: 1 << 20}},
			DropCapabilities: true,
			Capabilities:     []string{"CHOWN", "SETGID", "SETUID"},
		},
	})
	answers(t, ctx, rt, id, "wget", "-q", "-O", "/dev/null", "http://127.0.0.1:80/")

	if code, out, err := rt.Exec(ctx, id, []string{"touch", "/usr/share/nginx/html/new"}, 5*time.Second); err != nil || code == 0 || !strings.Contains(out, "Read-only file system") {
		t.Errorf("a write to the root filesystem: exit %d, %q, %v; want it refused as read-only", code, out, err)
	}
	for _, path := range []string{"/var/run/new", "/var/cache/nginx/new", "/data/new"} {
		if code, out, err := rt.Exec(ctx, id, []string{"touch", path}, 5*time.Second); err != nil || code != 0 {
			t.Errorf("touch %s: exit %d, %q, %v; want it writable", path, code, out, err)
		}
	}

	// /var/run is a link to /run in this image; the daemon mounts where it leads.
	var flags string
	for _, line := range strings.Split(execIn(t, ctx, rt, id, "cat", "/proc/mounts"), "\n") {
		if fields := strings.Fields(line); len(fields) >= 4 && fields[0] == "tmpfs" && fields[1] == "/run" {
			flags = fields[3]
		}
	}
	for _, flag := range []string{"rw", "nosuid", "nodev", "noexec", "size=1024k"} {
		if !strings.Contains(","+flags+",", ","+flag+",") {
			t.Errorf("the tmpfs is mounted with %q, want %s among them", flags, flag)
		}
	}
	// More than its size does not fit.
	if code, out, _ := rt.Exec(ctx, id, []string{"dd", "if=/dev/zero", "of=/var/run/big", "bs=1024", "count=2048"}, 10*time.Second); code == 0 || !strings.Contains(out, "No space left") {
		t.Errorf("2 MB into a tmpfs of 1 MB: exit %d, %q", code, out)
	}

	// CHOWN is bit 0, SETGID 6, SETUID 7.
	if got := effectiveCapabilities(t, ctx, rt, id); got != "00000000000000c1" {
		t.Errorf("CapEff = %s, want 00000000000000c1: the three kept and nothing else", got)
	}
	// KILL was dropped: root cannot signal a worker, which is another user's.
	if code, out, _ := rt.Exec(ctx, id, []string{"sh", "-c", "kill -0 $(pgrep -u nginx | head -n 1)"}, 5*time.Second); code == 0 || !strings.Contains(out, "not permitted") {
		t.Errorf("signalling another user's process without KILL: exit %d, %q", code, out)
	}

	// A restore writes a volume through the archive endpoint, which the
	// daemon closes for a read-only root and leaves open for its volumes.
	var archive bytes.Buffer
	tw := tar.NewWriter(&archive)
	body := []byte("restored")
	if err := tw.WriteHeader(&tar.Header{Name: "restored.txt", Mode: 0o644, Size: int64(len(body))}); err != nil {
		t.Fatal(err)
	}
	tw.Write(body)
	tw.Close()
	if err := rt.ImportPath(ctx, id, "/data", bytes.NewReader(archive.Bytes())); err != nil {
		t.Errorf("ImportPath into the volume of a read-only container: %v", err)
	}
	if got := execIn(t, ctx, rt, id, "cat", "/data/restored.txt"); got != "restored" {
		t.Errorf("the volume holds %q after the import", got)
	}
}

// A container that asks for nothing has Docker's default set; `capabilities:
// none` leaves root with nothing that makes it root.
func TestIntegrationDroppedCapabilitiesAreGone(t *testing.T) {
	rt, ctx, app := securityRuntime(t, testImage)
	sleep := ContainerSpec{App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: testImage, Entrypoint: []string{"sleep"}, Command: []string{"300"}}
	plain := started(t, ctx, rt, sleep)
	sleep.Replica, sleep.Security = 2, &Security{DropCapabilities: true}
	none := started(t, ctx, rt, sleep)
	sleep.Replica, sleep.Init = 3, true
	behindInit := started(t, ctx, rt, sleep)

	// The fourteen of the default set: what security.capabilities chooses from.
	if got := effectiveCapabilities(t, ctx, rt, plain); got != "00000000a80425fb" {
		t.Errorf("CapEff of a container that asked for nothing = %s, want Docker's default set, 00000000a80425fb", got)
	}
	for name, id := range map[string]string{"none": none, "none behind an init process": behindInit} {
		if got := effectiveCapabilities(t, ctx, rt, id); got != "0000000000000000" {
			t.Errorf("%s: CapEff = %s, want none", name, got)
		}
	}
	chown := []string{"chown", "1000", "/tmp"}
	if code, out, err := rt.Exec(ctx, plain, chown, 5*time.Second); err != nil || code != 0 {
		t.Errorf("chown with the default set: exit %d, %q, %v", code, out, err)
	}
	if code, out, _ := rt.Exec(ctx, none, chown, 5*time.Second); code == 0 || !strings.Contains(out, "not permitted") {
		t.Errorf("chown without CHOWN: exit %d, %q; want it refused", code, out)
	}
}

// What an application that needs nothing looks like: another user than root,
// a port above 1024, no capability, a root it cannot write and /tmp.
func TestIntegrationANodeApplicationRunsWithEverythingTakenAway(t *testing.T) {
	rt, ctx, app := securityRuntime(t, nodeImage)
	const server = `require("http").createServer((q, s) => {
  require("fs").writeFileSync(require("os").tmpdir() + "/hit", "1")
  s.end("ok")
}).listen(3000)`
	spec := ContainerSpec{
		App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: nodeImage,
		Command: []string{"node", "-e", server}, User: "1000:1000", Init: true,
		Security: &Security{ReadOnly: true, Tmpfs: []Tmpfs{{Path: "/tmp", SizeBytes: 16 << 20}}, DropCapabilities: true, NonRoot: true},
	}
	id := started(t, ctx, rt, spec)
	fetch := []string{"wget", "-q", "-O", "/dev/null", "http://127.0.0.1:3000/"}
	answers(t, ctx, rt, id, fetch...)
	if got := strings.TrimSpace(execIn(t, ctx, rt, id, "id", "-u")); got != "1000" {
		t.Errorf("the container runs as %q, want 1000", got)
	}
	if got := effectiveCapabilities(t, ctx, rt, id); got != "0000000000000000" {
		t.Errorf("CapEff = %s, want none", got)
	}

	// Without the tmpfs the same application has nowhere to write.
	spec.Replica, spec.Security = 2, &Security{ReadOnly: true, DropCapabilities: true, NonRoot: true}
	without := started(t, ctx, rt, spec)
	for deadline := time.Now().Add(20 * time.Second); ; time.Sleep(200 * time.Millisecond) {
		rt.Exec(ctx, without, fetch, 5*time.Second)
		if c, err := rt.InspectContainer(ctx, without); err == nil && !c.Running {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("the application kept running without a place to write")
		}
	}
	logs, _ := rt.Logs(ctx, without, 50)
	var output strings.Builder
	for _, l := range logs {
		output.WriteString(l.Message + "\n")
	}
	if !strings.Contains(output.String(), "EROFS") {
		t.Errorf("it stopped, but not over the read-only root:\n%s", output.String())
	}
}

func TestIntegrationNonRootRefusesAContainerThatWouldRunAsRoot(t *testing.T) {
	rt, ctx, app := securityRuntime(t, testImage)
	t.Cleanup(func() { rt.RemoveVolume(context.WithoutCancel(ctx), app, "data") })
	spec := ContainerSpec{
		App: app, DeploymentID: 1, Sequence: 1, Replica: 1, Image: testImage,
		Mounts:   []Mount{{Volume: "data", Path: "/data"}},
		Security: &Security{NonRoot: true},
	}

	// The image names no user.
	_, _, err := rt.CreateContainer(ctx, spec)
	var root *RootError
	if !errors.As(err, &root) || !root.FromImage || root.User != "" || !strings.Contains(err.Error(), "set user in deploy.yaml") {
		t.Fatalf("CreateContainer of %s under non_root: %v, want it refused with what to set", testImage, err)
	}
	if containers, err := rt.ListContainers(ctx, app); err != nil || len(containers) != 0 {
		t.Errorf("the refusal left %d containers behind (%v)", len(containers), err)
	}
	volumes, err := rt.ListVolumes(ctx)
	if err != nil {
		t.Fatalf("ListVolumes: %v", err)
	}
	for _, v := range volumes {
		if v.App == app {
			t.Errorf("the refusal left volume %s behind", v.Name)
		}
	}

	spec.User = "0"
	if _, _, err := rt.CreateContainer(ctx, spec); !errors.As(err, &root) || root.FromImage {
		t.Errorf("user 0 under non_root: %v, want it refused", err)
	}

	// A numeric user in deploy.yaml is enough, whatever the image says.
	spec.User, spec.Entrypoint, spec.Command = "101:101", []string{"sleep"}, []string{"300"}
	id := started(t, ctx, rt, spec)
	if got := strings.TrimSpace(execIn(t, ctx, rt, id, "id", "-u")); got != "101" {
		t.Errorf("the container runs as %q, want 101", got)
	}

	spec.Image, spec.Replica = "shipwick.invalid/none:1", 2
	if _, _, err := rt.CreateContainer(ctx, spec); !errors.Is(err, ErrNotFound) {
		t.Errorf("an image that is not there: %v, want ErrNotFound so that the caller pulls it", err)
	}
}
