package commands

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strconv"
	"strings"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/shipwick/shipwick/pkg/spec"
)

type initAnswers struct {
	Name   string
	Image  string
	Port   int
	Domain string
	// Project is what was recognised in the directory; nil with --image, or
	// when nothing was. It replaces Image with `build: .` or `static: <dir>`.
	Project *project
	// Init asks for an init process in front of the image's own: set for a
	// Dockerfile that init writes and that starts Node as the first process.
	Init bool
}

func (c *cli) initCommand() *cobra.Command {
	var file string
	var force bool
	var static string
	var answers initAnswers

	cmd := &cobra.Command{
		Use:   "init [dir]",
		Short: "Create a deploy.yaml, and a Dockerfile for a recognised project",
		Long: `Create a deploy.yaml in the current directory.

A Node (Nuxt, Next, SvelteKit, Remix, Astro, a server), .NET, Go or Python
project is recognised from its files: init writes a Dockerfile and a
.dockerignore for it, and a deploy.yaml with "build: ." so that "shipwick
deploy" builds the image here and sends it to the server. A folder of static
files (an index.html at the root, or in dist/, build/, out/, public/) and a
project whose build writes one get "static: <dir>": the proxy serves it, no
container. Existing Dockerfile and .dockerignore files are kept.

Where there is a shipwick.yaml and no deploy.yaml, init adds an entry to its
apps list instead, for the project in the current directory or in the one
given: "shipwick init web" writes web/Dockerfile and an entry with
"build: ./web". The file is appended to, never rewritten.

Run in a terminal, init asks for what it cannot tell (name, domain). With
--image it never prompts and writes no Dockerfile, which suits scripts:

  shipwick init --name my-api --image ghcr.io/company/my-api:1.0.0 --port 8080`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			// A shipwick.yaml is what deploy reads when there is no
			// deploy.yaml; a second file next to it would take its place.
			many := !cmd.Flags().Changed("file") && !exists(".", DefaultFile) && exists(".", spec.MultiFile)
			if !many && len(args) > 0 {
				return fmt.Errorf("a directory is for adding its project to a shipwick.yaml, and there is none here\n\nRun shipwick init inside %s instead", args[0])
			}
			if _, err := os.Stat(file); err == nil && !force {
				return fmt.Errorf("%s already exists\n\nEdit it, or overwrite it with: shipwick init --force", file)
			}
			if answers.Image != "" && static != "" {
				return errors.New("--image and --static exclude each other: an image runs in a container, a static folder is served by the proxy without one")
			}

			dir, named := filepath.Dir(file), "."
			if many {
				var err error
				if dir, err = entryDir(args, answers.Image != "" || static != ""); err != nil {
					return err
				}
				named = dir
			}
			if answers.Name == "" {
				answers.Name = defaultAppName(named)
			}
			switch {
			case answers.Image != "":
			case static != "":
				if answers.Port != 0 {
					return errors.New("--port does not apply to a static folder: the proxy serves its files, nothing listens")
				}
				answers.Project = &project{Kind: kindStatic, Label: "a static site (" + static + ")",
					Static: staticProject{Dir: filepath.ToSlash(filepath.Clean(static))}}
			default:
				p, err := detectProject(dir)
				if err != nil {
					return err
				}
				if p.Kind != "" {
					answers.Project = &p
				}
			}

			answers.Init = startsNode(answers.Project) && !exists(dir, "Dockerfile")

			switch {
			case answers.Project != nil:
				if err := c.completeProject(&answers, isTerminal(c.in)); err != nil {
					return err
				}
			case answers.Image == "" && !isTerminal(c.in):
				return errors.New("--image is required when not running in a terminal")
			case answers.Image == "":
				if err := c.promptInit(&answers); err != nil {
					return err
				}
			}

			if many {
				return c.initEntry(answers, dir)
			}

			content := renderConfig(answers)
			if _, err := spec.Parse([]byte(content)); err != nil {
				return err
			}
			written, kept, err := writeProjectFiles(dir, answers.Project)
			if err != nil {
				return err
			}
			if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
				return err
			}
			written = append(written, filepath.Base(file))

			p := answers.Project
			if p == nil {
				c.ui.Success("Created %s", file)
				c.ui.Println()
				c.ui.Println(c.initClosing(1))
				return nil
			}
			c.ui.Success("Recognised %s", p.Label)
			if len(kept) > 0 {
				c.ui.Success("Kept the existing %s; build: . will use %s", joinFiles(kept), itOrThem(len(kept)))
			}
			c.ui.Success("Wrote %s", joinFiles(written))
			c.noteNoLock(p, written)
			c.ui.Println()
			c.ui.Println(c.initClosing(len(written)))
			return nil
		},
	}
	fileFlag(cmd, &file)
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	cmd.Flags().StringVar(&answers.Name, "name", "", "application name (default: the directory name)")
	cmd.Flags().StringVar(&answers.Image, "image", "", "container image, e.g. ghcr.io/company/my-api:1.0.0; skips project detection")
	cmd.Flags().StringVar(&static, "static", "", "serve this folder of files as it is, e.g. dist/; skips project detection")
	cmd.Flags().IntVar(&answers.Port, "port", 0, "port the application listens on")
	cmd.Flags().StringVar(&answers.Domain, "domain", "", "public domain, e.g. api.example.com")
	return cmd
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

// prompter asks one question at a time on the terminal, taking the fallback
// on an empty answer.
type prompter struct {
	c  *cli
	in *bufio.Reader
}

func (p prompter) ask(label, fallback string) (string, error) {
	if fallback != "" {
		p.c.ui.Printf("%s [%s]: ", label, fallback)
	} else {
		p.c.ui.Printf("%s: ", label)
	}
	line, err := p.in.ReadString('\n')
	if err != nil && (err != io.EOF || line == "") {
		return "", errors.New("cancelled")
	}
	if line = strings.TrimSpace(line); line == "" {
		return fallback, nil
	}
	return line, nil
}

func (p prompter) askName(a *initAnswers) error {
	suggested := a.Name
	for {
		name, err := p.ask("Application name", a.Name)
		if err != nil {
			return err
		}
		if verr := spec.ValidateName(name); verr == nil {
			a.Name = name
			return nil
		}
		p.c.ui.Println("  Use lowercase letters, digits and dashes, e.g. my-api.")
		a.Name = suggested
	}
}

func (p prompter) askPort(a *initAnswers) error {
	for {
		raw, err := p.ask("Port the app listens on (empty for none)", "")
		if err != nil {
			return err
		}
		if raw == "" {
			return nil
		}
		if n, convErr := strconv.Atoi(raw); convErr == nil && n >= 1 && n <= 65535 {
			a.Port = n
			return nil
		}
		p.c.ui.Println("  Enter a number between 1 and 65535.")
	}
}

func (c *cli) promptInit(a *initAnswers) error {
	p := prompter{c, bufio.NewReader(c.in)}
	if err := p.askName(a); err != nil {
		return err
	}
	var err error
	for a.Image == "" {
		if a.Image, err = p.ask("Image (e.g. ghcr.io/company/"+a.Name+":1.0.0)", ""); err != nil {
			return err
		}
	}
	if a.Port == 0 {
		if err := p.askPort(a); err != nil {
			return err
		}
	}
	if a.Domain == "" && a.Port != 0 {
		if a.Domain, err = p.ask("Public domain (empty for none)", ""); err != nil {
			return err
		}
	}
	c.ui.Println()
	return nil
}

// completeProject fills in what detection cannot know about a recognised
// project. In a terminal it asks; otherwise the defaults stand, since a
// recognised project needs nothing more to deploy.
func (c *cli) completeProject(a *initAnswers, terminal bool) error {
	p := a.Project
	if a.Port != 0 && p.Kind != kindStatic {
		p.Port = a.Port
	}
	if !terminal {
		if len(p.Go.Mains) > 1 {
			return fmt.Errorf("several programs to choose from: %s\n\nRun shipwick init in a terminal to pick one", strings.Join(p.Go.Mains, ", "))
		}
		if p.Kind == kindStatic && a.Domain == "" {
			return errStaticNeedsDomain
		}
		a.Port = p.Port
		return nil
	}

	pr := prompter{c, bufio.NewReader(c.in)}
	if err := pr.askName(a); err != nil {
		return err
	}
	if len(p.Go.Mains) > 1 {
		if err := pr.askMain(p); err != nil {
			return err
		}
	}
	if p.Kind != kindStatic {
		if p.Port == 0 {
			if err := pr.askPort(a); err != nil {
				return err
			}
			p.Port = a.Port
		}
		a.Port = p.Port
	}
	switch {
	case a.Domain == "" && p.Kind == kindStatic:
		// The proxy serves the folder at its domain; without one there is
		// nothing to serve.
		var err error
		if a.Domain, err = pr.ask("Public domain", ""); err != nil {
			return err
		}
		if a.Domain == "" {
			return errStaticNeedsDomain
		}
	case a.Domain == "" && a.Port != 0:
		var err error
		if a.Domain, err = pr.ask("Public domain (empty for none)", ""); err != nil {
			return err
		}
	}
	c.ui.Println()
	return nil
}

var errStaticNeedsDomain = errors.New("a static site needs a domain: the proxy serves the files at it\n\nPass one with: shipwick init --domain example.com")

// askMain has the user pick which of several Go main packages to build.
func (p prompter) askMain(proj *project) error {
	p.c.ui.Println("Several programs found:")
	for i, m := range proj.Go.Mains {
		p.c.ui.Printf("  %d. %s\n", i+1, m)
	}
	for {
		raw, err := p.ask("Which one to deploy", "1")
		if err != nil {
			return err
		}
		if n, convErr := strconv.Atoi(raw); convErr == nil && n >= 1 && n <= len(proj.Go.Mains) {
			proj.Go.Package = proj.Go.Mains[n-1]
			proj.Label = goLabel(proj.Go.Package)
			return nil
		}
		p.c.ui.Printf("  Enter a number between 1 and %d.\n", len(proj.Go.Mains))
	}
}

// writeProjectFiles writes the Dockerfile and .dockerignore of a recognised
// project, unless they exist: a hand-written Dockerfile is the developer's
// and `build: .` uses it as it is; --force is about deploy.yaml only.
func writeProjectFiles(dir string, p *project) (written, kept []string, err error) {
	if p == nil || p.Kind == kindStatic {
		return nil, nil, nil
	}
	dockerfile, err := renderDockerfile(*p)
	if err != nil {
		return nil, nil, err
	}
	dockerignore, err := renderDockerignore(*p)
	if err != nil {
		return nil, nil, err
	}
	for _, f := range []struct{ name, content string }{{"Dockerfile", dockerfile}, {".dockerignore", dockerignore}} {
		if exists(dir, f.name) {
			kept = append(kept, f.name)
			continue
		}
		if err := os.WriteFile(filepath.Join(dir, f.name), []byte(f.content), 0o644); err != nil {
			return nil, nil, err
		}
		written = append(written, f.name)
	}
	return written, kept, nil
}

func itOrThem(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}

func joinFiles(names []string) string {
	if len(names) <= 1 {
		return strings.Join(names, "")
	}
	return strings.Join(names[:len(names)-1], ", ") + " and " + names[len(names)-1]
}

var invalidNameChars = regexp.MustCompile(`[^a-z0-9]+`)

// noteNoLock says what a Dockerfile written for a Node project without a
// lock file cannot promise.
func (c *cli) noteNoLock(p *project, written []string) {
	if !p.Node.NoLock || !slices.ContainsFunc(written, func(name string) bool { return path.Base(name) == "Dockerfile" }) {
		return
	}
	c.ui.Warn("no lock file: the Dockerfile installs with npm install, and the build is not reproducible until package-lock.json is committed")
}

// defaultAppName derives a valid application name from the name of dir, the
// directory the project is in.
func defaultAppName(dir string) string {
	const fallback = "my-app"
	dir, err := filepath.Abs(dir)
	if err != nil {
		return fallback
	}
	name := invalidNameChars.ReplaceAllString(strings.ToLower(filepath.Base(dir)), "-")
	name = strings.Trim(name, "-")
	if len(name) > 63 {
		name = strings.Trim(name[:63], "-")
	}
	if spec.ValidateName(name) != nil {
		return fallback
	}
	return name
}

// renderConfig writes the starter deploy.yaml: the answers as live settings,
// everything else as commented-out examples so the options are discoverable
// without being imposed.
func renderConfig(a initAnswers) string {
	var b strings.Builder
	line := func(format string, args ...any) { fmt.Fprintf(&b, format+"\n", args...) }

	line("name: %s", a.Name)
	line("")
	if p := a.Project; p != nil && p.Kind == kindStatic {
		return renderStaticConfig(&b, a)
	}

	if a.Project != nil {
		line("# The image is built on your machine by `shipwick deploy`, from the")
		line("# Dockerfile next to this file, and sent to the server. No registry needed.")
		line("build: .")
	} else {
		line("# Pin a version tag: deployments are recorded (and rolled back) by it.")
		line("image: %s", a.Image)
	}
	line("")
	line("# Run something other than the image's default. A string is one argument;")
	line("# use a list for several: nothing is split on spaces.")
	line("# entrypoint: [\"dotnet\"]")
	line("# command: [\"App.dll\", \"--urls\", \"http://0.0.0.0:8080\"]")
	line("# user: \"1000:1000\"")
	line("")
	if a.Init {
		line("# Node as a container's first process ignores SIGTERM unless the application")
		line("# handles it, and is killed when its grace period ends. An init process in")
		line("# front of it passes the signal on: a replaced replica stops at once.")
		line("init: true")
	} else {
		line("# Put an init process in front of the image's own, for a process that does")
		line("# not handle SIGTERM (Node started as `node server.js`). Not for an image")
		line("# that brings its own (tini, s6-overlay).")
		line("# init: true")
	}
	line("")
	if a.Port != 0 {
		line("# The port your application listens on inside the container.")
		line("port: %d", a.Port)
	} else {
		line("# The port your application listens on. Needed for `domain` and `health`.")
		line("# port: 8080")
	}
	line("")
	if a.Domain != "" {
		line("# Served over HTTPS automatically.")
		line("domain: %s", a.Domain)
	} else {
		line("# Public hostname, served over HTTPS automatically.")
		line("# domain: %s.example.com", a.Name)
	}
	line("")
	line("replicas: 1")
	line("")
	line("# ${NAME} is filled in from the environment or --env-file when you deploy,")
	line("# so that secrets never have to be in this file.")
	line("# env:")
	line("#   DATABASE_URL: postgres://app:${DATABASE_PASSWORD}@postgres:5432/app")
	line("")
	renderHealth(line, a)
	line("")
	line("# Per-replica limits. Unlimited when omitted.")
	line("# resources:")
	line("#   cpu: 1")
	line("#   memory: 512mb")
	line("")
	line("# Data that must outlive deployments (a database): named volumes, which")
	line("# need replicas: 1 and the recreate strategy.")
	line("# volumes:")
	line("#   - name: data")
	line("#     path: /var/lib/postgresql/data")
	line("# deploy:")
	line("#   strategy: recreate # rolling (default) | recreate")
	line("")
	line("# Run from the new image before its replicas start: database migrations.")
	line("# It runs next to the version still serving, so it must be compatible with it.")
	line("# pre_deploy:")
	line("#   command: [\"dotnet\", \"Migrate.dll\"]")
	line("#   timeout: 10m")
	line("")
	line("# Scheduled jobs: a one-off container from this image, on a cron schedule (UTC).")
	line("# jobs:")
	line("#   - name: nightly-report")
	line("#     schedule: \"0 3 * * *\"")
	line("#     command: [\"node\", \"report.js\"]")
	line("#     timeout: 1h")
	line("")
	line("restart:")
	line("  policy: always # always | on-failure | never")
	return b.String()
}

// renderHealth writes the health block: live for a framework known to answer
// its own root, otherwise a commented example at the endpoint the kind's
// community uses, with a hint on adding it.
func renderHealth(line func(string, ...any), a initAnswers) {
	p := a.Project
	if p != nil && p.HealthLive && a.Port != 0 {
		line("# A replica receives traffic only once this endpoint answers 2xx.")
		line("health:")
		line("  path: %s", p.HealthPath)
		return
	}

	path, hint := "/health", ""
	if p != nil {
		path = p.HealthPath
		switch p.Kind {
		case kindNode:
			hint = "Add a route that answers 200 on " + path + ", then uncomment."
		case kindDotnet:
			hint = "app.MapHealthChecks(\"" + path + "\") in Program.cs, then uncomment."
		case kindGo:
			hint = "Add a handler that answers 200 on " + path + ", then uncomment."
		case kindPython:
			hint = "Add a route that answers 200 on " + path + ", then uncomment."
		}
	}
	line("# A replica receives traffic only once this endpoint answers 2xx.")
	if hint != "" {
		line("# %s", hint)
	}
	line("# health:")
	line("#   path: %s", path)
	line("#   interval: 10s")
	line("#   timeout: 3s")
	line("#   retries: 3")
	if p != nil && p.Kind == kindDotnet {
		line("# A slow starter: failed checks do not count during this period.")
		line("#   start_period: 60s")
	}
	if p == nil {
		line("# Not an HTTP application? Instead of path, check that a port accepts")
		line("# connections, or run a command inside the replica (exit 0 is healthy):")
		line("#   tcp: 5432")
		line("#   command: [\"pg_isready\", \"-U\", \"postgres\"]")
	}
}

// renderStaticConfig is the short file of a folder served by the proxy: there
// is no container, so nothing about ports, replicas, env or restarts applies.
func renderStaticConfig(b *strings.Builder, a initAnswers) string {
	line := func(format string, args ...any) { fmt.Fprintf(b, format+"\n", args...) }
	s := a.Project.Static
	line("# A folder served by the proxy as it is: no image, no container, no port.")
	if s.Build != "" {
		line("# It is what `%s` produces; run that before `shipwick deploy`.", s.Build)
	}
	if s.Fallback != "" {
		line("# A single-page application: a path that names no file gets %s.", s.Fallback)
		line("static: {dir: %s, fallback: %s}", s.Dir, s.Fallback)
	} else {
		line("static: %s", s.Dir)
	}
	line("")
	line("# Served over HTTPS automatically.")
	line("domain: %s", a.Domain)
	return b.String()
}
