// Package commands implements the shipwick command tree.
package commands

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/cliconfig"
	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
	"github.com/shipwick/shipwick/pkg/version"
)

// DefaultFile is the config file shipwick looks for in the current directory.
const DefaultFile = "deploy.yaml"

// ErrReported marks a failure that has already been explained on screen; the
// process should exit non-zero without printing anything more.
var ErrReported = errors.New("reported")

// Options are the process-level dependencies, injectable for tests.
type Options struct {
	In     io.Reader
	Out    io.Writer
	Err    io.Writer
	Getenv func(string) string
}

// cli is the state shared by all commands.
type cli struct {
	ui     *ui.UI
	in     io.Reader
	getenv func(string) string
	now    func() time.Time

	flagURL string
	// pollInterval paces `deploy` while it waits for the agent.
	pollInterval time.Duration
}

// NewRootCommand builds the shipwick command tree.
func NewRootCommand(opts Options) *cobra.Command {
	_, root := newRoot(opts)
	return root
}

// newRoot also returns the shared state, which tests adjust (clock, polling).
func newRoot(opts Options) (*cli, *cobra.Command) {
	c := &cli{
		ui:           ui.New(opts.Out, opts.Err, opts.Getenv),
		in:           opts.In,
		getenv:       opts.Getenv,
		now:          time.Now,
		pollInterval: 500 * time.Millisecond,
	}

	root := &cobra.Command{
		Use:   "shipwick",
		Short: "Deploy and manage applications on a Shipwick server",
		Long: `shipwick deploys and manages applications on a Shipwick server.

Describe your application in deploy.yaml, then:

  shipwick deploy

The agent is found through --url, SHIPWICK_AGENT_URL, or the config written by
"shipwick login" (default: ` + cliconfig.DefaultURL + `). The API token comes from
SHIPWICK_AGENT_TOKEN or that same config; it is never accepted as a flag.`,
		Version:       version.Version,
		SilenceUsage:  true, // a failed deploy is not a usage error
		SilenceErrors: true, // main renders errors, see Render
	}
	root.SetIn(opts.In)
	root.SetOut(opts.Out)
	root.SetErr(opts.Err)
	root.PersistentFlags().StringVar(&c.flagURL, "url", "", "agent URL (overrides SHIPWICK_AGENT_URL and the saved config)")

	root.AddCommand(
		c.initCommand(),
		c.validateCommand(),
		c.deployCommand(),
		c.redeployCommand(),
		c.rollbackCommand(),
		c.statusCommand(),
		c.psCommand(),
		c.logsCommand(),
		c.stopCommand(),
		c.startCommand(),
		c.deleteCommand(),
		c.serverCommand(),
		c.loginCommand(),
	)
	return c, root
}

// connect builds a client for the configured agent.
func (c *cli) connect() (*client.Client, error) {
	path, err := cliconfig.Path(c.getenv)
	if err != nil {
		return nil, err
	}
	file, err := cliconfig.Load(path)
	if err != nil {
		return nil, err
	}
	cfg := cliconfig.Resolve(c.flagURL, c.getenv, file)

	cl, err := client.New(cfg.URL, cfg.Token)
	if err != nil {
		return nil, err
	}
	if cl.SendsTokenInCleartext() {
		c.ui.Warn("sending the API token over unencrypted HTTP to %s — use HTTPS or an SSH tunnel", cl.URL())
	}
	return cl, nil
}

// fileFlag registers the -f/--file flag shared by commands that read deploy.yaml.
func fileFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVarP(target, "file", "f", DefaultFile, "path to the deployment config")
}

// readConfig loads and validates a deploy.yaml, returning both the raw
// document (what is sent to the agent) and its parsed form.
func readConfig(path string) ([]byte, spec.App, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, spec.App{}, err
	}
	app, err := spec.Parse(data)
	return data, app, err
}

func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s not found\n\nCreate one with: shipwick init", path)
	}
	return data, err
}

// resolveApp determines which application a command targets: the explicit
// argument, or else the one described by the config file.
func resolveApp(args []string, file string) (string, error) {
	if len(args) > 0 {
		if err := spec.ValidateName(args[0]); err != nil {
			return "", err
		}
		return args[0], nil
	}
	_, app, err := readConfig(file)
	if err != nil {
		var verr *spec.ValidationError
		if errors.As(err, &verr) {
			return "", err
		}
		return "", fmt.Errorf("no application given, and %s was not found here\n\nName one explicitly, e.g.: shipwick status my-api", file)
	}
	return app.Name, nil
}

// Render turns an error into what the user should read. It returns "" for
// errors that were already reported.
func Render(err error) string {
	if err == nil || errors.Is(err, ErrReported) {
		return ""
	}

	var verr *spec.ValidationError
	if errors.As(err, &verr) {
		return verr.Error()
	}
	if verr, ok := client.ValidationError(err); ok {
		return verr.Error()
	}

	var unreachable *client.UnreachableError
	if errors.As(err, &unreachable) {
		return unreachable.Error() + `

Is the agent running? If it is on a remote server, open a tunnel first:
  ssh -L 9000:127.0.0.1:9000 user@your-server
or point shipwick at it with --url / ` + cliconfig.EnvURL + `.`
	}

	var apiErr *client.APIError
	if errors.As(err, &apiErr) {
		switch apiErr.Code {
		case api.CodeUnauthorized:
			return `The agent rejected the API token.

Set ` + cliconfig.EnvToken + `, or save it with: shipwick login`
		case api.CodeDeploymentInProgress:
			return "Another operation is already in progress for this application.\n\nWatch it with: shipwick status"
		case api.CodeEndpointNotFound:
			return "The agent does not know this operation — it is probably older than this shipwick.\n\nCompare versions with: shipwick server status"
		case api.CodeNotFound:
			return "The server does not know that application.\n\nList what it runs with: shipwick ps"
		case api.CodeNotDeployed:
			return "This application has no successful deployment yet.\n\nDeploy it with: shipwick deploy"
		case api.CodeNoRollbackTarget:
			return "There is no earlier successful deployment to go back to.\n\nSee the history with: shipwick status"
		}
		return "Error: " + apiErr.Message
	}

	return "Error: " + strings.TrimSpace(err.Error())
}
