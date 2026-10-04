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

	flagURL     string
	flagContext string
	// target is what connect resolved, for output that names the server.
	target cliconfig.Target
	// severalContexts is set by connect when the config file holds more than
	// one server; only then is the context name worth showing.
	severalContexts bool
	// pollInterval paces `deploy` while it waits for the agent.
	pollInterval time.Duration
	// upgrade is where `shipwick upgrade` looks and what it replaces; tests
	// point it at a fake GitHub and a file of their own.
	upgrade upgradeOptions
	// local is what reaches beyond the agent API from this machine: ssh, the
	// browser, DNS, TCP; see small.go. Tests replace it.
	local localOptions
	// deployFollowsInit is set while deploy runs init for a directory without
	// a deploy.yaml, so that init's closing line does not send the user to a
	// command that is already running.
	deployFollowsInit bool
	// manyInFlight is set on the copies of this state that deploy the entries
	// of a shipwick.yaml: the closing hints are printed once, not per entry.
	manyInFlight bool
	// build is how `shipwick deploy` builds and saves an image on this
	// machine for an application with `build:`; tests substitute fakes.
	build buildTools
	// running is the command being executed, for an error that shows it
	// with what was missing.
	running *cobra.Command
	// verbose is `deploy --verbose`: everything docker build prints, in a
	// terminal too.
	verbose bool
	// after calls f once d has passed and returns how to call that off;
	// time.AfterFunc when nil. Tests replace it so that nothing waits.
	after func(d time.Duration, f func()) (stop func())
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
SHIPWICK_AGENT_TOKEN or that same config; it is never accepted as a flag.

Several servers are saved as contexts: "shipwick login --context staging" adds
one, --context or SHIPWICK_CONTEXT selects one for a command, and "shipwick
context use" changes the current one.`,
		Version:       version.Version,
		SilenceUsage:  true, // a failed deploy is not a usage error
		SilenceErrors: true, // main renders errors, see Render
		// A binary that upgraded itself on Windows leaves its predecessor
		// behind, because the running executable cannot be deleted.
		PersistentPreRunE: func(cmd *cobra.Command, _ []string) error {
			c.running = cmd
			removeStaleExecutable()
			return c.useOutbound()
		},
	}
	root.SetIn(opts.In)
	root.SetOut(opts.Out)
	root.SetErr(opts.Err)
	root.PersistentFlags().StringVar(&c.flagURL, "url", "", "agent URL (overrides SHIPWICK_AGENT_URL and the saved config)")
	root.PersistentFlags().StringVar(&c.flagContext, "context", "", "saved server to use (overrides SHIPWICK_CONTEXT and the current context)")

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
	root.AddCommand(c.jobCommands()...)
	root.AddCommand(c.volumeCommands()...)
	root.AddCommand(c.tokenCommands()...)
	root.AddCommand(c.upgradeCommands()...)
	root.AddCommand(c.secretCommands()...)
	root.AddCommand(c.smallCommands()...)
	root.AddCommand(c.trafficCommands()...)
	root.AddCommand(c.registryCommands()...)
	root.AddCommand(c.certCommands()...)
	root.AddCommand(c.exportCommands()...)
	root.AddCommand(c.backupCommands()...)
	root.AddCommand(c.auditCommands()...)
	root.AddCommand(c.accessCommands()...)
	root.AddCommand(c.configCommand())
	return c, root
}

// resolve determines which agent, and with which token, this command talks to.
func (c *cli) resolve() (cliconfig.Target, error) {
	_, file, err := c.loadSaved()
	if err != nil {
		return cliconfig.Target{}, err
	}
	target, err := cliconfig.Resolve(c.flagURL, c.flagContext, c.getenv, file)
	if err != nil {
		return cliconfig.Target{}, err
	}
	c.target = target
	c.severalContexts = len(file.Contexts) > 1
	return target, nil
}

// connect builds a client for the configured agent.
func (c *cli) connect() (*client.Client, error) {
	target, err := c.resolve()
	if err != nil {
		return nil, err
	}
	cl, err := client.New(target.URL, target.Token)
	if err != nil {
		return nil, err
	}
	if cl.SendsTokenInCleartext() {
		c.ui.Warn("sending the API token over unencrypted HTTP to %s — use HTTPS or an SSH tunnel", c.describeServer(cl.URL()))
	}
	return cl, nil
}

// describeServer names the server a command talks to: its URL, and the
// context it came from when there are several to tell apart.
func (c *cli) describeServer(url string) string {
	if c.severalContexts && c.target.Context != "" {
		return url + " (context " + c.target.Context + ")"
	}
	return url
}

// fileFlag registers the -f/--file flag shared by commands that read deploy.yaml.
func fileFlag(cmd *cobra.Command, target *string) {
	cmd.Flags().StringVarP(target, "file", "f", DefaultFile, "path to the deployment config")
}

// nameOnly parses a deploy.yaml for its application name.
func (c *cli) nameOnly(path string) (spec.App, error) {
	data, err := readFile(path)
	if err != nil {
		return spec.App{}, err
	}
	data, _, err = expand(data, c.standIn)
	if err != nil {
		return spec.App{}, err
	}
	return spec.Parse(data)
}

// standIn fills a placeholder for a command that wants only the name in the
// file: from the environment when it is set there, otherwise with a word that
// lets the rest of the file parse.
func (c *cli) standIn(name string) (string, bool) {
	if v := c.getenv(name); v != "" {
		return v, true
	}
	return "placeholder", true
}

func readFile(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("%s not found\n\nCreate one with: shipwick init", path)
	}
	return data, err
}

// resolveApp determines which application a command targets: the explicit
// argument, or else the one described by the config file. Without a
// deploy.yaml, a shipwick.yaml answers when it describes one application;
// when it describes several, the user has to say which.
func (c *cli) resolveApp(args []string, file string) (string, error) {
	if len(args) > 0 {
		if err := spec.ValidateName(args[0]); err != nil {
			return "", err
		}
		return args[0], nil
	}
	missing := file
	if file == DefaultFile && !exists(".", DefaultFile) && exists(".", spec.MultiFile) {
		file = spec.MultiFile
	}
	// Only the name is wanted. Placeholders that are not set here — a CI
	// secret, an image tag — are filled with a stand-in so that the rest of
	// the file still parses.
	names, err := c.namesOnly(file)
	if err != nil {
		var verr *spec.ValidationError
		if errors.As(err, &verr) {
			return "", err
		}
		return "", fmt.Errorf("no application given, and %s was not found here\n\nName one explicitly, e.g.: %s", missing, c.commandWith("my-api"))
	}
	if len(names) > 1 {
		return "", fmt.Errorf("%s describes %d applications: %s\n\nSay which one, e.g.: %s", file, len(names), strings.Join(names, ", "), c.commandWith(names[0]))
	}
	return names[0], nil
}

// namesOnly parses a config file for the applications it names: one for a
// deploy.yaml, one per entry for a shipwick.yaml.
func (c *cli) namesOnly(path string) ([]string, error) {
	data, err := readFile(path)
	if err != nil {
		return nil, err
	}
	if !spec.IsMany(data) {
		app, err := c.nameOnly(path)
		return []string{app.Name}, err
	}
	data, _, err = expand(data, c.standIn)
	if err != nil {
		return nil, err
	}
	entries, err := spec.ParseMany(data)
	if err != nil {
		return nil, err
	}
	names := make([]string, len(entries))
	for i, e := range entries {
		names[i] = e.App.Name
	}
	return names, nil
}

// commandWith is the running command as it is typed with an application's
// name: "shipwick status api", "shipwick run api -- <command> [args...]".
func (c *cli) commandWith(name string) string {
	if c.running == nil || !strings.Contains(c.running.Use, "[app]") {
		return "shipwick status " + name
	}
	return c.running.Parent().CommandPath() + " " + strings.Replace(c.running.Use, "[app]", name, 1)
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
		if unreachable.ProxyStatus != 0 {
			return unreachable.Error() + `

The proxy is up and the agent behind it is not: it is restarting, or stopped.
Try again in a moment. If it stays away, on the server:
  cd /opt/shipwick && docker compose ps`
		}
		if hint := onServerHint(unreachable.URL); hint != "" {
			return unreachable.Error() + "\n\n" + hint
		}
		return unreachable.Error() + `

Is the agent running? If it is on a remote server, open a tunnel first:
  ssh -L 9000:127.0.0.1:9000 user@your-server
or point shipwick at it with --url / ` + cliconfig.EnvURL + `, or at another
saved server with --context (see: shipwick context ls).`
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
			// The bare "not found" is an unknown application; anything more
			// specific — a volume, a job, a run — says what it is.
			if apiErr.Message != "" && apiErr.Message != "not found" {
				return "Error: " + apiErr.Message
			}
			return "The server does not know that application.\n\nList what it runs with: shipwick ps"
		case api.CodeNotDeployed:
			return "This application has no successful deployment yet.\n\nDeploy it with: shipwick deploy"
		case api.CodeNoRollbackTarget:
			return "There is no earlier successful deployment to go back to.\n\nSee the history with: shipwick status"
		case api.CodeForbidden:
			return renderForbidden(apiErr)
		case api.CodeApplicationRunning:
			return "The application is running, and a restore replaces the files under it.\n\nStop it first with: shipwick stop"
		case api.CodeJobAlreadyRunning:
			return "This job is still running from an earlier start.\n\nSee it with: shipwick jobs <app>"
		case api.CodeStaticApplication:
			return "This application is a folder served by the proxy: it has no containers, so there are no logs, metrics or commands to run.\n\nSee what it serves with: shipwick status"
		case api.CodeVolumeInUse:
			if app, _ := apiErr.Details["application"].(string); app != "" {
				return "Error: " + apiErr.Message + "\n\nDelete it with: shipwick delete " + app
			}
			return "Error: " + apiErr.Message
		case api.CodeRateLimited:
			return "Too many failed attempts from this address; try again in a minute."
		case api.CodeImageIncomplete:
			return "The server no longer has the layers that were left out of the image.\n\nSend it again with: shipwick deploy"
		case api.CodeRegistryLoginFailed:
			if refused, _ := apiErr.Details["refused"].(bool); refused {
				return "Error: " + apiErr.Message + "\n\nNothing was stored. Check the username and the token, and that the token may read images; then log in again."
			}
			return "Error: " + apiErr.Message + "\n\nNothing was stored. Check the registry's name, and that the server can reach it."
		case api.CodeKeyRotationPending:
			file, _ := apiErr.Details["key_file"].(string)
			return "The key was already rotated, and the agent has not been restarted with the new one.\n\nPut the key from " + file + " on the server into /opt/shipwick/.env as " + agentKeyVariable + ",\nrestart the agent, then rotate again."
		case api.CodeInvalidCertificate:
			return "The server refused the certificate: " + apiErr.Message + ".\n\n--cert is the chain in PEM, the hostname's own certificate first (fullchain.pem); --key is its private key (privkey.pem)."
		case api.CodeTrafficUnavailable:
			return "This server records no traffic: the agent reads the access log of the Caddy container in its own compose project, and there is none.\n\nSee how the proxy is doing with: shipwick server status"
		case api.CodeBackupBusy:
			return "That backup is in use: it is still being taken, verified or restored.\n\nSee where it stands with: shipwick backups <app>"
		case api.CodeBackupNotUsable:
			return "Error: " + apiErr.Message + "\n\nSee which backups succeeded with: shipwick backups <app>"
		case api.CodeNoVolumes:
			return "This application has no volumes, so there is nothing to back up.\n\nGive it some under `volumes` in deploy.yaml."
		case api.CodeBackupsNotEncrypted:
			return "Error: " + apiErr.Message + "\n\nSet SHIPWICK_BACKUP_PASSPHRASE in /opt/shipwick/.env on the server, then: cd /opt/shipwick && docker compose up -d"
		case api.CodeImportInProgress:
			return "An import is already running on this server; it takes one at a time.\n\nFollow it with: shipwick import --status"
		case api.CodeInvalidExport:
			return "Error: " + apiErr.Message + "\n\nWhat the import had finished before it stopped is in place: shipwick import --status. An export is written with: shipwick export"
		case api.CodeExportInProgress:
			return "The server is writing an export already.\n\nSee it with: shipwick export --list"
		case api.CodeStandbyNotConfigured:
			return "This server has no bucket to fetch exports from.\n\nSet the SHIPWICK_BACKUP_S3_* variables and SHIPWICK_BACKUP_PASSPHRASE of the first server, and SHIPWICK_STANDBY_SCHEDULE, in /opt/shipwick/.env here; or import a file with: shipwick import <file> --stopped"
		case api.CodeTokenExpired:
			return renderTokenExpired(apiErr)
		case api.CodeTokenLimited:
			return renderTokenLimited(apiErr)
		case api.CodePromotionInProgress:
			return "This server is being promoted; nothing is imported into it meanwhile.\n\nFollow the promotion with: shipwick standby promote"
		case api.CodeSessionExpired, api.CodeSessionEnded:
			return renderSessionOver(apiErr)
		}
		return "Error: " + apiErr.Message
	}

	return "Error: " + strings.TrimSpace(err.Error())
}
