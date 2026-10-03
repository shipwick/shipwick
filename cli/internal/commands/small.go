package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"os/exec"
	"runtime"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// localOptions are the ways a command reaches past the agent's API: a
// program on this machine, a name server, a port on the server. Zero fields
// take their defaults; tests fill them in so that nothing leaves the process.
type localOptions struct {
	// exec runs a program on this machine as an argv — never through a
	// shell, so nothing in it is interpreted twice.
	exec func(ctx context.Context, argv []string, stdout, stderr io.Writer) error
	// lookupHost resolves a hostname as the public internet sees it.
	lookupHost func(ctx context.Context, host string) ([]string, error)
	// dial tries a TCP connection to host:port.
	dial func(ctx context.Context, address string) error
	// http fetches an application's front page.
	http *http.Client
	// interactive tells whether a person is at the keyboard; nil means
	// "when standard input is a terminal".
	interactive func() bool
	goos        string
	// sleep waits between retries; tests make it return at once.
	sleep func(time.Duration)
}

func (o localOptions) withDefaults() localOptions {
	if o.exec == nil {
		o.exec = runProgram
	}
	if o.lookupHost == nil {
		o.lookupHost = publicLookupHost
	}
	if o.dial == nil {
		o.dial = func(ctx context.Context, address string) error {
			d := net.Dialer{Timeout: portCheckTimeout}
			conn, err := d.DialContext(ctx, "tcp", address)
			if err != nil {
				return err
			}
			return conn.Close()
		}
	}
	if o.http == nil {
		o.http = &http.Client{Timeout: 10 * time.Second}
	}
	if o.goos == "" {
		o.goos = runtime.GOOS
	}
	if o.sleep == nil {
		o.sleep = time.Sleep
	}
	return o
}

// portCheckTimeout is how long doctor waits for a port on the server; a
// firewall that drops packets answers with silence.
const portCheckTimeout = 3 * time.Second

func runProgram(ctx context.Context, argv []string, stdout, stderr io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	return cmd.Run()
}

// interactive reports whether a command may ask questions.
func (c *cli) interactive() bool {
	if c.local.interactive != nil {
		return c.local.interactive()
	}
	return isTerminal(c.in)
}

// smallCommands returns the doctor, open commands.
func (c *cli) smallCommands() []*cobra.Command {
	return []*cobra.Command{c.doctorCommand(), c.openCommand()}
}

func (c *cli) openCommand() *cobra.Command {
	var file string
	var dashboard bool
	cmd := &cobra.Command{
		Use:   "open [app]",
		Short: "Open the application, or the dashboard, in the browser",
		Long: `Open https://<domain> of the application in the browser.

Without an argument, the application described by deploy.yaml is opened; in a
directory without one, the server's dashboard. --dashboard opens the dashboard
wherever you are.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if dashboard {
				if len(args) > 0 {
					return errors.New("--dashboard opens the dashboard, not an application; leave the name out")
				}
				return c.openDashboard(cmd.Context(), nil)
			}
			name, err := c.resolveApp(args, file)
			if err != nil {
				// No application was meant by name or by file: the dashboard
				// is the one address left that the server has. A shipwick.yaml
				// with several applications is a file that means some: the
				// error that asks which one stands.
				_, statErr := os.Stat(file)
				_, manyErr := os.Stat(spec.MultiFile)
				if len(args) == 0 && !cmd.Flags().Changed("file") && errors.Is(statErr, fs.ErrNotExist) && errors.Is(manyErr, fs.ErrNotExist) {
					return c.openDashboard(cmd.Context(), err)
				}
				return err
			}
			return c.open(cmd.Context(), name)
		},
	}
	fileFlag(cmd, &file)
	cmd.Flags().BoolVar(&dashboard, "dashboard", false, "open the server's dashboard")
	return cmd
}

func (c *cli) open(ctx context.Context, name string) error {
	cl, err := c.connect()
	if err != nil {
		return err
	}
	app, err := cl.Application(ctx, name)
	if err != nil {
		return err
	}
	if app.Domain == "" {
		return errors.New(noDomain(app))
	}
	if spec.IsWildcard(app.Domain) {
		return fmt.Errorf("%s serves %s, which is every name under the domain and no address of its own; open one of them yourself", app.Name, app.Domain)
	}
	return c.openURL(ctx, "https://"+app.Domain+app.Path)
}

// noDashboard is what a server without a dashboard hostname is answered with.
const noDashboard = "This server has no dashboard hostname. Set SHIPWICK_DASHBOARD_DOMAIN in /opt/shipwick/.env and run the installer again"

// openDashboard opens the address the agent reports for the dashboard. When
// there is none, the answer is otherwise, if the caller has one: the error
// that sent it here.
func (c *cli) openDashboard(ctx context.Context, otherwise error) error {
	cl, err := c.connect()
	if err != nil {
		return err
	}
	info, err := cl.Server(ctx)
	if err != nil {
		return err
	}
	if info.DashboardURL == "" {
		if otherwise != nil {
			return otherwise
		}
		return errors.New(noDashboard)
	}
	return c.openURL(ctx, info.DashboardURL)
}

func (c *cli) openURL(ctx context.Context, url string) error {
	local := c.local.withDefaults()
	argv := openerArgv(local.goos, url)
	if err := local.exec(ctx, argv, io.Discard, io.Discard); err != nil {
		if errors.Is(err, exec.ErrNotFound) {
			return fmt.Errorf("%s was not found, so no browser could be opened\n\nOpen it yourself: %s", argv[0], url)
		}
		return fmt.Errorf("%s could not open the browser: %w\n\nOpen it yourself: %s", argv[0], err, url)
	}
	c.ui.Println("Opening " + url)
	return nil
}

// noDomain explains where an application without a domain can be reached:
// by name, from the other applications on the server, and nowhere else.
func noDomain(app api.ApplicationDetail) string {
	if app.Spec != nil && app.Spec.Port != 0 {
		return fmt.Sprintf("%s has no domain; it is reachable from other applications at http://%s:%d\n\nTo serve it publicly, add domain: to deploy.yaml and deploy again", app.Name, app.Name, app.Spec.Port)
	}
	return fmt.Sprintf("%s has no domain; other applications reach it by the name %s on the server\n\nTo serve it publicly, add port: and domain: to deploy.yaml and deploy again", app.Name, app.Name)
}

// openerArgv is the platform's own way to hand a URL to the default
// browser. The URL is one argument of a program, not part of a command line.
func openerArgv(goos, url string) []string {
	switch goos {
	case "windows":
		return []string{"rundll32", "url.dll,FileProtocolHandler", url}
	case "darwin":
		return []string{"open", url}
	}
	return []string{"xdg-open", url}
}

// initIfMissing is what `deploy` does in a directory without a deploy.yaml:
// in a terminal it runs init first, so that a first deployment is one
// command rather than a failure and a second command. Off a terminal, deploy
// fails as before, and that failure names init.
func (c *cli) initIfMissing(ctx context.Context) error {
	if _, err := os.Stat(DefaultFile); !errors.Is(err, fs.ErrNotExist) || !c.interactive() {
		return nil
	}
	c.ui.Println("No deploy.yaml here. Let's write one.")
	c.ui.Println()
	if err := c.initHere(ctx); err != nil {
		return err
	}
	c.ui.Println()
	return nil
}

// initHere runs `shipwick init` as if typed without flags: through the
// command, so that its flags keep their defaults and this stays one call
// away from its code.
func (c *cli) initHere(ctx context.Context) error {
	c.deployFollowsInit = true
	defer func() { c.deployFollowsInit = false }()
	cmd := c.initCommand()
	cmd.SetContext(ctx)
	return cmd.RunE(cmd, nil)
}

// initClosing is init's last line. On its own, init hands over to deploy;
// when deploy is what called it, the deployment follows at once and the
// user is told they can still change the files.
func (c *cli) initClosing(files int) string {
	if c.deployFollowsInit {
		return "Deploying now. Change " + itOrThem(files) + " later and deploy again."
	}
	return "Review " + itOrThem(files) + ", then run: shipwick deploy"
}

// printNextSteps closes an application's first deployment with the two
// commands that answer the next question: is it running, and what is it
// saying. A static application has no logs; its next question is what the
// site looks like. A server with a dashboard adds where it is: a first
// deployment is when its address is news.
func (c *cli) printNextSteps(name string, static bool, dashboardURL string) {
	c.ui.Println()
	c.ui.Println("Next:")
	first, firstWhy := "  shipwick logs -f "+name, "follow the logs"
	status, statusWhy := "  shipwick status "+name, "replicas, health, history"
	if static {
		first, firstWhy = "  shipwick open "+name, "open it in the browser"
		statusWhy = "what it serves, history"
	}
	dashboard := "  " + dashboardURL
	width := max(len(first), len(status)) + 4
	if dashboardURL != "" {
		width = max(width, len(dashboard)+4)
	}
	c.ui.Printf("%-*s%s\n", width, first, firstWhy)
	c.ui.Printf("%-*s%s\n", width, status, statusWhy)
	if dashboardURL != "" {
		c.ui.Printf("%-*s%s\n", width, dashboard, "the dashboard")
	}
}
