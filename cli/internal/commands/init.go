package commands

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
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
}

func (c *cli) initCommand() *cobra.Command {
	var file string
	var force bool
	var answers initAnswers

	cmd := &cobra.Command{
		Use:   "init",
		Short: "Create a deploy.yaml in the current directory",
		Long: `Create a deploy.yaml in the current directory.

Run in a terminal, init asks for what it needs. With --image it never prompts,
which suits scripts:

  shipwick init --name my-api --image ghcr.io/company/my-api:1.0.0 --port 8080`,
		Args: cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			if _, err := os.Stat(file); err == nil && !force {
				return fmt.Errorf("%s already exists\n\nEdit it, or overwrite it with: shipwick init --force", file)
			}

			if answers.Name == "" {
				answers.Name = defaultAppName()
			}
			if answers.Image == "" {
				if !isTerminal(c.in) {
					return errors.New("--image is required when not running in a terminal")
				}
				if err := c.promptInit(&answers); err != nil {
					return err
				}
			}

			content := renderConfig(answers)
			if _, err := spec.Parse([]byte(content)); err != nil {
				return err
			}
			if err := os.WriteFile(file, []byte(content), 0o644); err != nil {
				return err
			}
			c.ui.Success("Created %s", file)
			c.ui.Println()
			c.ui.Println("Review it, then run: shipwick deploy")
			return nil
		},
	}
	fileFlag(cmd, &file)
	cmd.Flags().BoolVar(&force, "force", false, "overwrite an existing file")
	cmd.Flags().StringVar(&answers.Name, "name", "", "application name (default: the directory name)")
	cmd.Flags().StringVar(&answers.Image, "image", "", "container image, e.g. ghcr.io/company/my-api:1.0.0")
	cmd.Flags().IntVar(&answers.Port, "port", 0, "port the application listens on")
	cmd.Flags().StringVar(&answers.Domain, "domain", "", "public domain, e.g. api.example.com")
	return cmd
}

func isTerminal(r io.Reader) bool {
	f, ok := r.(*os.File)
	return ok && term.IsTerminal(int(f.Fd()))
}

func (c *cli) promptInit(a *initAnswers) error {
	in := bufio.NewReader(c.in)
	ask := func(label, fallback string) (string, error) {
		if fallback != "" {
			c.ui.Printf("%s [%s]: ", label, fallback)
		} else {
			c.ui.Printf("%s: ", label)
		}
		line, err := in.ReadString('\n')
		if err != nil && (err != io.EOF || line == "") {
			return "", errors.New("cancelled")
		}
		if line = strings.TrimSpace(line); line == "" {
			return fallback, nil
		}
		return line, nil
	}

	var err error
	for {
		if a.Name, err = ask("Application name", a.Name); err != nil {
			return err
		}
		if verr := spec.ValidateName(a.Name); verr == nil {
			break
		}
		c.ui.Println("  Use lowercase letters, digits and dashes, e.g. my-api.")
		a.Name = defaultAppName()
	}
	for a.Image == "" {
		if a.Image, err = ask("Image (e.g. ghcr.io/company/"+a.Name+":1.0.0)", ""); err != nil {
			return err
		}
	}
	if a.Port == 0 {
		for {
			raw, err := ask("Port the app listens on (empty for none)", "")
			if err != nil {
				return err
			}
			if raw == "" {
				break
			}
			if n, convErr := strconv.Atoi(raw); convErr == nil && n >= 1 && n <= 65535 {
				a.Port = n
				break
			}
			c.ui.Println("  Enter a number between 1 and 65535.")
		}
	}
	if a.Domain == "" && a.Port != 0 {
		if a.Domain, err = ask("Public domain (empty for none)", ""); err != nil {
			return err
		}
	}
	c.ui.Println()
	return nil
}

var invalidNameChars = regexp.MustCompile(`[^a-z0-9]+`)

// defaultAppName derives a valid application name from the directory name.
func defaultAppName() string {
	const fallback = "my-app"
	dir, err := os.Getwd()
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
	line("# Pin a version tag: deployments are recorded (and rolled back) by it.")
	line("image: %s", a.Image)
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
	line("# env:")
	line("#   DATABASE_URL: postgres://user:password@host:5432/db")
	line("")
	line("# A replica receives traffic only once this endpoint answers 2xx.")
	line("# health:")
	line("#   path: /health")
	line("#   interval: 10s")
	line("#   timeout: 3s")
	line("#   retries: 3")
	line("")
	line("# Per-replica limits. Unlimited when omitted.")
	line("# resources:")
	line("#   cpu: 1")
	line("#   memory: 512mb")
	line("")
	line("restart:")
	line("  policy: always # always | on-failure | never")
	return b.String()
}
