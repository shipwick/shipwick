package commands

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os/exec"
	"path"
	"path/filepath"
	"strings"
	"sync"
	"time"

	dockerclient "github.com/moby/moby/client"
	"go.yaml.in/yaml/v3"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/spec"
)

// An application with `build:` has no registry. `shipwick deploy` builds the
// image here, where the project and Docker are, saves it and sends it to the
// agent, which loads it and deploys it like any other. The server never
// builds: it has neither the source nor the time, and a Dockerfile runs what
// it likes.

// buildTools are what building needs from this machine. Zero fields take
// their defaults; tests substitute a fake `docker` and a fake image store.
type buildTools struct {
	// run runs a program on this machine — `docker build` — with the given
	// argv, in dir, its output going to out. Never a shell.
	run func(ctx context.Context, dir string, argv []string, out io.Writer) error
	// save streams an image from the local Docker daemon in `docker save`
	// format.
	save func(ctx context.Context, image string) (io.ReadCloser, error)
	// layers returns an image's layers as the local Docker daemon knows
	// them: diff IDs, base layer first. See layers.go.
	layers func(ctx context.Context, image string) ([]string, error)
}

func (b buildTools) withDefaults() buildTools {
	if b.layers == nil {
		// A substituted image store has no Docker behind it to ask.
		b.layers = noImageLayers
		if b.save == nil {
			b.layers = dockerImageLayers
		}
	}
	if b.run == nil {
		b.run = func(ctx context.Context, dir string, argv []string, out io.Writer) error {
			cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
			cmd.Dir = dir
			cmd.Stdout, cmd.Stderr = out, out
			return cmd.Run()
		}
	}
	if b.save == nil {
		b.save = func(ctx context.Context, image string) (io.ReadCloser, error) {
			cli, err := dockerclient.New(dockerclient.FromEnv)
			if err != nil {
				return nil, fmt.Errorf("connect to Docker: %w", err)
			}
			rc, err := cli.ImageSave(ctx, []string{image})
			if err != nil {
				cli.Close()
				return nil, fmt.Errorf("save %s: %w", image, err)
			}
			return &closeBoth{ReadCloser: rc, cli: cli}, nil
		}
	}
	return b
}

// closeBoth closes the Docker client with the stream it produced.
type closeBoth struct {
	io.ReadCloser
	cli *dockerclient.Client
}

func (c *closeBoth) Close() error {
	err := c.ReadCloser.Close()
	c.cli.Close()
	return err
}

// buildImage builds the application's image here, sends it to the server and
// returns the deploy.yaml document with `image` set to the reference the
// server answered — what is then deployed, exactly as if the file had named it.
func (c *cli) buildImage(ctx context.Context, cl *client.Client, file string, data []byte, app spec.App) ([]byte, error) {
	tools := c.build.withDefaults()

	if err := c.askFirst(ctx, cl, app, data, "built"); err != nil {
		return nil, err
	}

	server, err := cl.Server(ctx)
	if err != nil {
		return nil, err
	}
	platform, err := dockerPlatform(server.Architecture)
	if err != nil {
		return nil, err
	}
	image := spec.LocalImagePrefix(app.Name) + c.now().UTC().Format("20060102-150405") + "-" + randomHex(2)

	// The paths in deploy.yaml are relative to the file, so docker runs in
	// its directory; `-f` is relative to that directory, not to the context.
	// Both paths are validated relative paths and the reference is ours:
	// nothing here came from anywhere a shell could see.
	argv := []string{"docker", "build", "--platform", platform, "-f", path.Join(app.Build.Context, app.Build.Dockerfile), "-t", image, app.Build.Context}
	if err := c.runBuild(ctx, tools, filepath.Dir(file), argv); err != nil {
		return nil, err
	}
	c.ui.Success("Built %s for %s", image, platform)

	loaded, err := c.sendImage(ctx, cl, tools, app.Name, image)
	if err != nil {
		return nil, err
	}
	return overrideImage(data, loaded.Image), nil
}

// askFirst has the agent validate the document before anything is built or
// uploaded. What the agent refuses for reasons only it knows — a domain
// another application serves, a port already published, a secret that is not
// stored — it would otherwise refuse after the build and the upload. It
// checks again when the deployment starts. An agent older than the operation
// is asked the one thing an older shipwick checked itself: whether the
// domain is free. spared completes that message: "built", "uploaded".
func (c *cli) askFirst(ctx context.Context, cl *client.Client, app spec.App, data []byte, spared string) error {
	if supported, err := cl.Validate(ctx, app.Name, data); supported || err != nil {
		return err
	}
	if app.Domain == "" {
		return nil
	}
	return c.domainIsFree(ctx, cl, app, spared)
}

// runBuild runs `docker build` and shows it the way its reader needs it. A
// pipeline's log is read afterwards and gets every line, as does --verbose.
// A person at a terminal gets one line that says the build is moving, and
// everything only when it fails; several applications sharing a terminal
// have no such line, so their builds are silent until they end.
func (c *cli) runBuild(ctx context.Context, tools buildTools, dir string, argv []string) error {
	if c.verbose || !c.ui.Watched() {
		c.ui.Println(c.ui.Styled(ui.Dim, "$ "+strings.Join(argv, " ")))
		tail := &tailWriter{}
		err := tools.run(ctx, dir, argv, io.MultiWriter(&dimWriter{ui: c.ui}, tail))
		return buildError(ctx, err, tail.String())
	}

	started := c.now()
	out := &buildOutput{show: func(last string) {
		line := "Building the image (" + elapsed(c.now().Sub(started)) + ")"
		if last != "" {
			line += " — " + last
		}
		// "… " comes before it, and a line that reaches the last column wraps.
		c.ui.Progress("%s", truncate(line, max(c.ui.Width()-3, 20)))
	}}
	out.refresh()
	// docker can be silent for a long step; the time keeps saying it runs.
	done := make(chan struct{})
	ticking := make(chan struct{})
	go func() {
		defer close(ticking)
		tick := time.NewTicker(time.Second)
		defer tick.Stop()
		for {
			select {
			case <-tick.C:
				out.refresh()
			case <-done:
				return
			}
		}
	}()
	err := tools.run(ctx, dir, argv, out)
	close(done)
	<-ticking
	c.ui.Done()

	if err = buildError(ctx, err, ""); err != nil && ctx.Err() == nil && !errors.Is(err, errNoDocker) {
		c.ui.Println(c.ui.Styled(ui.Dim, "$ "+strings.Join(argv, " ")))
		c.ui.Printf("%s", c.ui.Styled(ui.Dim, out.String()))
	}
	return err
}

var errNoDocker = errors.New("docker is not installed on this machine, and build: needs it here (the server never builds); install Docker Desktop or set image: instead")

// buildError says how `docker build` failed; tail is the end of its output
// when that is not on screen already.
func buildError(ctx context.Context, err error, tail string) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, exec.ErrNotFound):
		return errNoDocker
	case ctx.Err() != nil:
		return ctx.Err()
	case tail == "":
		return fmt.Errorf("docker build failed: %w", err)
	}
	return fmt.Errorf("docker build failed: %w\n\n%s", err, tail)
}

// buildOutput keeps everything `docker build` printed, for the case that it
// fails, and passes the last line on as it changes.
type buildOutput struct {
	mu   sync.Mutex
	all  strings.Builder
	rest string // the line still being written
	last string
	show func(last string)
}

func (w *buildOutput) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.all.Write(p)
	w.rest += string(p)
	for {
		line, rest, ok := strings.Cut(w.rest, "\n")
		if !ok {
			break
		}
		w.rest = rest
		// A line docker rewrote in place ends with its last version.
		if i := strings.LastIndexByte(strings.TrimRight(line, "\r"), '\r'); i >= 0 {
			line = line[i+1:]
		}
		if line = printable(line); line != "" {
			w.last = line
		}
	}
	w.show(w.last)
	return len(p), nil
}

func (w *buildOutput) refresh() {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.show(w.last)
}

// String is the whole output, ending in a newline when there is any.
func (w *buildOutput) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	s := w.all.String()
	if s != "" && !strings.HasSuffix(s, "\n") {
		s += "\n"
	}
	return s
}

// printable is a line of another program's output made fit for the progress
// line: no escape sequences' control characters, no surrounding space.
func printable(line string) string {
	return strings.TrimSpace(strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, line))
}

// elapsed renders a running time in whole seconds: "12s", "1m05s".
func elapsed(d time.Duration) string {
	s := int(d.Seconds())
	if s < 60 {
		return fmt.Sprintf("%ds", s)
	}
	return fmt.Sprintf("%dm%02ds", s/60, s%60)
}

// dockerPlatform turns the architecture the server reports (uname -m) into
// what `docker build --platform` takes, so that a laptop of one kind builds
// for a server of another.
func dockerPlatform(architecture string) (string, error) {
	platforms := map[string]string{
		"x86_64":  "linux/amd64",
		"amd64":   "linux/amd64",
		"aarch64": "linux/arm64",
		"arm64":   "linux/arm64",
		"armv7l":  "linux/arm/v7",
		"armv6l":  "linux/arm/v6",
		"i686":    "linux/386",
		"ppc64le": "linux/ppc64le",
		"s390x":   "linux/s390x",
		"riscv64": "linux/riscv64",
	}
	if p, ok := platforms[architecture]; ok {
		return p, nil
	}
	if architecture == "" {
		return "", errors.New("the server does not report its architecture, so the image cannot be built for it; upgrade the agent, or set image: instead")
	}
	return "", fmt.Errorf("the server's architecture %q is not one docker build can target from here; set image: instead", architecture)
}

func randomHex(n int) string {
	b := make([]byte, n)
	rand.Read(b)
	return hex.EncodeToString(b)
}

// refuseImageOverride is the check `deploy --image` needs before the file is
// parsed with the override in it: a file with `build:` is built here, and
// another image next to it would only be rejected by validation with a message
// about a line the user never wrote.
func refuseImageOverride(path string) error {
	data, err := readFile(path)
	if err != nil {
		return err
	}
	var doc struct {
		Build yaml.Node `yaml:"build"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Build.IsZero() {
		return nil // whatever else is wrong, loadConfig reports it
	}
	return fmt.Errorf("%s has build: and is built here, so --image does not apply; remove build: to deploy an image instead", path)
}

// describeImage is the "where does the image come from" part of a validate
// summary: the build, or the image and its version.
func describeImage(app spec.App) [][2]string {
	var fields [][2]string
	if b := app.Build; b != nil {
		context := b.Context + "/"
		if b.Context == "." {
			context = "./"
		}
		fields = append(fields, [2]string{"Build", fmt.Sprintf("%s (%s)", context, b.Dockerfile)})
	}
	if app.Image != "" {
		fields = append(fields, [2]string{"Image", app.Image}, [2]string{"Version", app.Version()})
	}
	return fields
}

// noteBuild follows a validate summary of an application with `build:`, which
// validate does not build.
func (c *cli) noteBuild(app spec.App) {
	if app.Build == nil {
		return
	}
	c.ui.Println()
	c.ui.Println("shipwick deploy builds the image on this machine and sends it to the server; no registry is involved.")
}

// dimWriter shows a program's output dimmed: the build is worth watching, but
// it is not shipwick's own voice.
type dimWriter struct{ ui *ui.UI }

func (w *dimWriter) Write(p []byte) (int, error) {
	w.ui.Printf("%s", w.ui.Styled(ui.Dim, string(p)))
	return len(p), nil
}

// tailWriter keeps the last lines of what was written, for an error message
// that must stand on its own.
type tailWriter struct {
	lines []string
	rest  string
}

const tailLines = 10

func (w *tailWriter) Write(p []byte) (int, error) {
	w.rest += string(p)
	for {
		line, rest, ok := strings.Cut(w.rest, "\n")
		if !ok {
			break
		}
		w.lines, w.rest = append(w.lines, line), rest
		if len(w.lines) > tailLines {
			w.lines = w.lines[1:]
		}
	}
	return len(p), nil
}

func (w *tailWriter) String() string {
	lines := w.lines
	if w.rest != "" {
		lines = append(lines, w.rest)
	}
	return strings.TrimRight(strings.Join(lines, "\n"), "\n")
}

// progressReader reports how many bytes have passed, once per megabyte: often
// enough to see the upload move, rarely enough not to flood the terminal.
type progressReader struct {
	r      io.Reader
	n      int64
	report func(n int64)
}

func (p *progressReader) Read(buf []byte) (int, error) {
	n, err := p.r.Read(buf)
	before := p.n >> 20
	p.n += int64(n)
	if p.n>>20 != before {
		p.report(p.n)
	}
	return n, err
}

// domainIsFree asks the server which hostnames its other applications serve
// and reports the first of this application's that one of them already does.
// Two applications may serve one hostname under different paths; a hostname
// that is redirected is taken whole. It is what is asked of an agent too old
// to validate a document itself.
func (c *cli) domainIsFree(ctx context.Context, cl *client.Client, app spec.App, spared string) error {
	others, err := cl.Applications(ctx)
	if err != nil {
		return nil // the deploy itself will say what is wrong
	}
	if taken, owner := takenHostname(app, others); taken != "" {
		return fmt.Errorf("%s is already served by application %q; nothing was %s\n\nUse another domain or another path, or change or delete that application first: shipwick delete %s", taken, owner, spared, owner)
	}
	return nil
}
