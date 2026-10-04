package commands

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/spec"
)

// DefaultParallel is how many applications of a shipwick.yaml deploy at the
// same time. Each one pulls an image and starts containers on the same
// server; four keep a small server busy without starving any of them.
const DefaultParallel = 4

// manyFile decides whether this command works on a shipwick.yaml — several
// applications in one file — and returns its path, or "" for the ordinary
// one-file-per-application case. With no -f at all, deploy.yaml is looked for
// first and shipwick.yaml second, so a project with one file of either kind
// needs no flag.
func (c *cli) manyFile(cmd *cobra.Command, files []string) (string, error) {
	if !cmd.Flags().Changed("file") {
		if _, err := os.Stat(DefaultFile); errors.Is(err, os.ErrNotExist) {
			if _, err := os.Stat(spec.MultiFile); err == nil {
				return spec.MultiFile, nil
			}
		}
		return "", nil
	}
	for _, file := range files {
		data, err := readFile(file)
		if err != nil {
			return "", err
		}
		if !spec.IsMany(data) {
			continue
		}
		if len(files) > 1 {
			return "", fmt.Errorf("%s describes several applications and cannot be combined with other files\n\nDeploy it on its own: shipwick deploy -f %s", file, file)
		}
		return file, nil
	}
	return "", nil
}

// loadMany is loadConfig for a shipwick.yaml. Placeholders are filled in over
// the whole file, so ${NAME} works in every entry.
func (c *cli) loadMany(path string, envFiles []string) (entries []spec.Entry, vars placeholders, err error) {
	data, err := readFile(path)
	if err != nil {
		return nil, placeholders{}, err
	}
	files, err := loadEnvFiles(envFiles)
	if err != nil {
		return nil, placeholders{}, err
	}
	data, vars, err = expand(data, c.lookup(files))
	if err != nil {
		return nil, placeholders{}, fmt.Errorf("%s: %w", path, err)
	}
	entries, err = spec.ParseMany(data)
	return entries, vars, err
}

// deployMany deploys the applications of a shipwick.yaml in dependency order,
// several at a time. It reports whether the command was about such a file at
// all; if not, deploy proceeds as with any deploy.yaml.
func (c *cli) deployMany(ctx context.Context, cmd *cobra.Command, files, envFiles, names []string, image string, noWait bool, parallel int) (bool, error) {
	file, err := c.manyFile(cmd, files)
	if err != nil {
		return true, err
	}
	if file == "" {
		return false, nil
	}
	if image != "" && len(names) == 0 {
		return true, fmt.Errorf("--image applies to one application, and %s describes several\n\nName the one it is for: shipwick deploy <name> --image %s", file, image)
	}
	if parallel < 1 {
		return true, errors.New("--parallel must be at least 1")
	}

	all, vars, err := c.loadMany(file, envFiles)
	if err != nil {
		return true, err
	}
	chosen, err := selectEntries(file, all, names)
	if err != nil {
		return true, err
	}
	if image != "" {
		if err := chosen.withImage(image); err != nil {
			return true, err
		}
	}
	entries := chosen.entries
	plain := make([][]string, len(entries))
	for i, e := range entries {
		plain[i] = vars.plainOf(chosen.index[i], e.App)
	}
	cl, err := c.connect()
	if err != nil {
		return true, err
	}

	// One application reads exactly as it does from its own deploy.yaml.
	if len(entries) == 1 {
		c.ui.Println("Deploying " + c.ui.Styled(ui.Bold, entries[0].App.Name) + "...")
		c.ui.Println()
		c.ui.Success("Validated %s%s", file, substitutedNote(vars))
		if line := chosen.assumedLine(); line != "" {
			c.ui.Println(line)
		}
		if o := c.deployEntry(ctx, cl, file, entries[0], plain[0], c.ui, noWait); o != deployed && o != begun {
			return true, ErrReported
		}
		return true, nil
	}

	c.ui.Println(fmt.Sprintf("Deploying %d applications...", len(entries)))
	c.ui.Println()
	c.ui.Success("Validated %s%s", file, substitutedNote(vars))
	if line := chosen.assumedLine(); line != "" {
		c.ui.Println(line)
	}
	c.ui.Println()
	return true, c.runMany(ctx, cl, file, entries, plain, noWait, parallel)
}

// runMany drives the schedule: one goroutine per application in flight, the
// next one started as soon as a slot is free and its dependencies are done.
func (c *cli) runMany(ctx context.Context, cl *client.Client, file string, entries []spec.Entry, plain [][]string, noWait bool, parallel int) error {
	names := make([]string, len(entries))
	width := 0
	for i, e := range entries {
		names[i] = e.App.Name
		width = max(width, len(e.App.Name))
	}
	// Without waiting, "deployed" can never be observed, so only the
	// applications that wait for nothing are started; starting is one
	// request each, and the limit is about what runs on the server.
	if noWait {
		parallel = len(entries)
	}
	s := newSchedule(entries, parallel)

	type result struct {
		app     int
		outcome outcome
	}
	results := make(chan result)
	for {
		start, skips := s.next()
		for _, sk := range skips {
			c.ui.Println(fmt.Sprintf("Skipped %s: %s did not deploy", names[sk.app], sk.blame))
		}
		for _, i := range start {
			prefixed := c.ui.Prefixed(c.ui.Styled(ui.Cyan, fmt.Sprintf("%-*s", width, names[i])) + "  ")
			go func() {
				results <- result{i, c.deployEntry(ctx, cl, file, entries[i], plain[i], prefixed, noWait)}
			}()
		}
		if noWait {
			s.halt()
		}
		if s.inFlight() == 0 {
			break
		}
		r := <-results
		s.finish(r.app, r.outcome)
		if ctx.Err() != nil {
			// Ctrl-C: what runs on the server continues, nothing new starts.
			s.halt()
		}
	}

	if noWait {
		for i, o := range s.state {
			if o == pending {
				c.ui.Println(fmt.Sprintf("Not started: %s waits for %s", names[i], strings.Join(entries[i].After, ", ")))
			}
		}
	}
	c.ui.Println()
	c.ui.Println(s.summary(noWait))
	if s.count(failed)+s.count(skipped)+s.count(interrupted) > 0 {
		return ErrReported
	}
	return nil
}

// deployEntry deploys one application of a shipwick.yaml, narrating through
// u, and says how it went. Nothing it returns is left unexplained on screen.
func (c *cli) deployEntry(ctx context.Context, cl *client.Client, file string, e spec.Entry, plain []string, u *ui.UI, noWait bool) outcome {
	// followDeployment narrates through c.ui; a copy of the command state
	// with the prefixed UI keeps one implementation for one and for many.
	cc := *c
	cc.ui = u
	cc.manyInFlight = true

	// An entry with build: is built here first, like a deploy.yaml is; its
	// paths, like a static entry's folder, are relative to the shipwick.yaml.
	config := e.Config
	var err error
	if e.App.Build != nil {
		if config, err = cc.buildImage(ctx, cl, file, e.Config, e.App); err != nil {
			u.Failure("%s", strings.TrimSpace(Render(explainUnknownKeys(ctx, cl, err))))
			return failed
		}
	}

	started := c.now()
	// A static entry uploads its folder first, as a deploy.yaml does.
	d, err := cc.startDeployment(ctx, cl, file, e.App, config, plain)
	if err == nil {
		err = cc.followDeployment(ctx, cl, d, started, noWait)
	}
	switch {
	case err == nil && noWait:
		return begun
	case err == nil:
		return deployed
	case ctx.Err() != nil:
		return interrupted
	case errors.Is(err, ErrReported):
		return failed
	}
	// The request itself was refused — the application is busy, the agent
	// found a mistake — which followDeployment never got to explain.
	u.Failure("%s", strings.TrimSpace(Render(explainUnknownKeys(ctx, cl, err))))
	return failed
}

// outcome is where one application of a shipwick.yaml stands.
type outcome int

const (
	pending outcome = iota
	running
	deployed
	begun       // started with --no-wait; whether it deploys is not known here
	failed      // the deployment failed, or was rolled back
	skipped     // a dependency did not deploy
	interrupted // the wait was stopped; the deployment continues on the server
)

// schedule orders the applications of one shipwick.yaml by their dependencies.
// It knows nothing of servers or goroutines — only what has finished and how —
// so that who starts when can be tested as a table.
type schedule struct {
	entries []spec.Entry
	index   map[string]int
	state   []outcome
	limit   int
	halted  bool
}

func newSchedule(entries []spec.Entry, limit int) *schedule {
	s := &schedule{entries: entries, index: map[string]int{}, state: make([]outcome, len(entries)), limit: limit}
	for i, e := range entries {
		s.index[e.App.Name] = i
	}
	return s
}

type skip struct {
	app   int
	blame string // the dependency that did not deploy
}

// next marks every pending application whose dependency will never deploy as
// skipped, and returns those plus the ones to start now: every dependency
// deployed and a slot free, in file order.
func (s *schedule) next() (start []int, skips []skip) {
	// A skip can cascade — web after api after postgres — so repeat until
	// nothing changes.
	for changed := true; changed; {
		changed = false
		for i, e := range s.entries {
			if s.state[i] != pending {
				continue
			}
			for _, dep := range e.After {
				if o := s.state[s.index[dep]]; o == failed || o == skipped {
					s.state[i] = skipped
					skips = append(skips, skip{i, dep})
					changed = true
					break
				}
			}
		}
	}
	if s.halted {
		return nil, skips
	}
	for i, e := range s.entries {
		if s.state[i] != pending || s.inFlight() >= s.limit {
			continue
		}
		ready := true
		for _, dep := range e.After {
			if s.state[s.index[dep]] != deployed {
				ready = false
			}
		}
		if ready {
			s.state[i] = running
			start = append(start, i)
		}
	}
	return start, skips
}

func (s *schedule) finish(i int, o outcome) { s.state[i] = o }

// halt stops next from starting anything more; what is running finishes.
func (s *schedule) halt() { s.halted = true }

func (s *schedule) inFlight() int { return s.count(running) }

func (s *schedule) count(o outcome) int {
	n := 0
	for _, st := range s.state {
		if st == o {
			n++
		}
	}
	return n
}

// summary is the last line: "3 of 3 applications deployed." when all went
// well, otherwise what happened to the rest.
func (s *schedule) summary(noWait bool) string {
	total := len(s.entries)
	if noWait {
		return fmt.Sprintf("%d of %d applications started.", s.count(begun), total)
	}
	line := fmt.Sprintf("%d of %d applications deployed", s.count(deployed), total)
	var rest []string
	for _, part := range []struct {
		o    outcome
		noun string
	}{{failed, "failed"}, {skipped, "skipped"}, {interrupted, "still deploying"}, {pending, "not started"}} {
		if n := s.count(part.o); n > 0 {
			rest = append(rest, fmt.Sprintf("%d %s", n, part.noun))
		}
	}
	if len(rest) == 0 {
		return line + "."
	}
	return "Stopped: " + line + ", " + strings.Join(rest, ", ") + "."
}

// validateMany is validate for a shipwick.yaml: every application's summary
// in file order, preceded by the order they deploy in. With names, the whole
// file is still checked, and what is shown is what a deployment of those
// names would do.
func (c *cli) validateMany(cmd *cobra.Command, files, envFiles, names []string) (bool, error) {
	file, err := c.manyFile(cmd, files)
	if err != nil {
		return true, err
	}
	if file == "" {
		return false, nil
	}
	all, vars, err := c.loadMany(file, envFiles)
	if err != nil {
		return true, err
	}
	chosen, err := selectEntries(file, all, names)
	if err != nil {
		return true, err
	}
	entries := chosen.entries
	c.ui.Success("%s is valid%s", file, substitutedNote(vars))
	c.ui.Println()
	if len(entries) > 1 {
		width := 0
		for _, e := range entries {
			width = max(width, len(e.App.Name))
		}
		for _, e := range entries {
			line := c.ui.Styled(ui.Bold, e.App.Name)
			if len(e.After) > 0 {
				line += strings.Repeat(" ", width-len(e.App.Name)+2) + c.ui.Styled(ui.Dim, "after ") + strings.Join(e.After, ", ")
			}
			c.ui.Println(line)
		}
		c.ui.Println()
	}
	if len(chosen.assumed) > 0 {
		c.ui.Println(fmt.Sprintf("shipwick deploy %s leaves out %s and assumes %s running.", strings.Join(names, " "), strings.Join(chosen.assumed, ", "), pluralIt(len(chosen.assumed))))
		c.ui.Println()
	}
	for i, e := range entries {
		if i > 0 {
			c.ui.Println()
		}
		c.ui.Fields(describeSpec(e.App))
	}
	return true, nil
}
