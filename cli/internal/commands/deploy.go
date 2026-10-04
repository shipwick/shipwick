package commands

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"go.yaml.in/yaml/v3"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// maxPollFailures is how many consecutive failed status polls `deploy`
// tolerates (an agent restart, a network blip) before it stops waiting.
const maxPollFailures = 20

func (c *cli) deployCommand() *cobra.Command {
	var files, envFiles []string
	var image string
	var noWait bool
	var parallel int

	cmd := &cobra.Command{
		Use:   "deploy [name...]",
		Short: "Deploy the application described by deploy.yaml",
		Long: `Deploy the application described by deploy.yaml and wait for the result.

Replicas are replaced one at a time, each new one only after it proved healthy,
so the application keeps serving throughout. If the new version fails, the
deployment is undone — replicas already replaced are restored — and the
command exits non-zero.

In CI, keep deploy.yaml in the repository and supply the freshly built image:

  shipwick deploy --image ghcr.io/company/my-api:$GIT_SHA

Values that must not be in the file — passwords, API keys — are written as
${NAME} and filled in from the environment or from --env-file before the file
is sent. An env value whose name is set nowhere here is left to the server,
which fills it in from its secrets (shipwick secret set); anywhere else such a
name is an error.

Several applications in one shipwick.yaml (an "apps" list; "after" names the
ones an entry waits for) deploy at the same time, in dependency order; it is
used when there is no deploy.yaml. Several deploy.yaml files deploy in the
order given, one after the other:

  shipwick deploy -f api/deploy.yaml -f worker/deploy.yaml -f web/deploy.yaml

Names deploy those applications of a shipwick.yaml and nothing else, which is
what a pipeline wants after it built one image:

  shipwick deploy api --image ghcr.io/company/api:$GIT_SHA

"after" still orders the named ones among themselves. An application that is
left out is not waited for: it is assumed to be running, and the output says
so. --image applies when exactly one is named.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, names []string) error {
			if handled, err := c.deployMany(cmd.Context(), cmd, files, envFiles, names, image, noWait, parallel); handled {
				return err
			}
			if len(names) > 0 {
				return refuseNames(cmd, files)
			}
			if image != "" && len(files) > 1 {
				return errors.New("--image applies to one application; deploy several with one deploy.yaml each and no --image")
			}
			if !cmd.Flags().Changed("file") {
				if err := c.initIfMissing(cmd.Context()); err != nil {
					return err
				}
			}
			return c.deploy(cmd.Context(), files, envFiles, image, noWait)
		},
	}
	filesFlag(cmd, &files, &envFiles)
	cmd.Flags().StringVar(&image, "image", "", "deploy this image instead of the one in the config")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "start the deployment and return immediately")
	cmd.Flags().IntVar(&parallel, "parallel", DefaultParallel, "how many applications of a shipwick.yaml deploy at the same time")
	cmd.Flags().BoolVar(&c.verbose, "verbose", false, "show everything docker build prints, instead of one progress line")
	return cmd
}

// filesFlag is fileFlag for commands that take several configs.
func filesFlag(cmd *cobra.Command, files, envFiles *[]string) {
	cmd.Flags().StringArrayVarP(files, "file", "f", []string{DefaultFile}, "path to a deployment config; repeat for several applications")
	cmd.Flags().StringArrayVar(envFiles, "env-file", nil, "NAME=value file for ${NAME} placeholders; repeat for several (env values may also come from the server's secrets)")
}

// deploy deploys every file in order and stops at the first failure: what
// comes later usually depends on what came before.
func (c *cli) deploy(ctx context.Context, files, envFiles []string, image string, noWait bool) error {
	// Every file is read and validated before the first deployment starts,
	// so that a typo in the third does not leave the first two half done.
	type loaded struct {
		file string
		data []byte
		app  spec.App
		vars placeholders
	}
	configs := make([]loaded, 0, len(files))
	for _, file := range files {
		if image != "" {
			if err := refuseImageOverride(file); err != nil {
				return err
			}
		}
		data, app, vars, err := c.loadConfig(file, envFiles, image)
		if err != nil {
			return err
		}
		configs = append(configs, loaded{file, data, app, vars})
	}

	cl, err := c.connect()
	if err != nil {
		return err
	}

	for i, cfg := range configs {
		if i > 0 {
			c.ui.Println()
		}
		c.ui.Println("Deploying " + c.ui.Styled(ui.Bold, cfg.app.Name) + "...")
		c.ui.Println()
		c.ui.Success("Validated %s%s", cfg.file, substitutedNote(cfg.vars))
		if cfg.app.Build != nil {
			if cfg.data, err = c.buildImage(ctx, cl, cfg.file, cfg.data, cfg.app); err != nil {
				return explainUnknownKeys(ctx, cl, err)
			}
		}

		started := c.now()
		d, err := c.startDeployment(ctx, cl, cfg.file, cfg.app, cfg.data, cfg.vars.plainOf(0, cfg.app))
		if err != nil {
			return explainUnknownKeys(ctx, cl, err)
		}
		if err := c.followDeployment(ctx, cl, d, started, noWait); err != nil {
			if len(configs) > 1 && errors.Is(err, ErrReported) {
				c.ui.Println()
				c.ui.Println(fmt.Sprintf("Stopped at %s: %d of %d applications deployed.", cfg.app.Name, i, len(configs)))
			}
			return err
		}
	}
	if len(configs) > 1 {
		c.ui.Println()
		c.ui.Println(fmt.Sprintf("%d of %d applications deployed.", len(configs), len(configs)))
	}
	return nil
}

func substitutedNote(vars placeholders) string {
	return vars.note()
}

func plural(n int, noun string) string {
	if n == 1 {
		return fmt.Sprintf("%d %s", n, noun)
	}
	return fmt.Sprintf("%d %ss", n, noun)
}

// followDeployment is the second half of every command that starts a
// deployment — deploy, redeploy, rollback: wait for it, narrate it, report
// the outcome, and turn a failure into a non-zero exit.
func (c *cli) followDeployment(ctx context.Context, cl *client.Client, d api.Deployment, started time.Time, noWait bool) error {
	name := d.Application
	if noWait {
		c.ui.Success("Deployment #%d started", d.Sequence)
		c.ui.Println()
		c.ui.Println("Follow it with: shipwick status " + name)
		return nil
	}

	final, err := c.awaitDeployment(ctx, cl, d.ID, d.Static != nil)
	if err != nil {
		if ctx.Err() != nil {
			c.ui.Println()
			c.ui.Println("Stopped waiting. The deployment continues on the server:")
			c.ui.Println("  shipwick status " + name)
			return ErrReported
		}
		return err
	}

	if final.Status != api.StatusActive {
		c.reportFailedDeployment(ctx, cl, final)
		return ErrReported
	}

	c.ui.Println()
	c.ui.Println(c.ui.Styled(ui.Bold, name) + " " + final.Version + c.ui.Styled(ui.Dim, "  deployed in "+ui.Duration(c.now().Sub(started))))
	if detail, err := cl.Application(ctx, name); err == nil {
		c.ui.Println(describeServing(detail))
	}
	if final.Spec.Domain != "" {
		c.ui.Println("https://" + final.Spec.Domain + final.Spec.Path)
	}
	if final.Sequence == 1 && !c.manyInFlight {
		// An agent that cannot be asked, or does not say, costs the line
		// about the dashboard and nothing else.
		info, _ := cl.Server(ctx)
		c.printNextSteps(name, final.Spec.Static != nil, info.DashboardURL)
	}
	return nil
}

// awaitDeployment polls until the agent is done with the deployment, echoing
// its events as they appear. "Done" is completed_at, not the first settled
// status: until then the application still refuses new operations. static
// picks the progress labels of a folder deployment.
func (c *cli) awaitDeployment(ctx context.Context, cl *client.Client, id int64, static bool) (api.DeploymentDetail, error) {
	defer c.ui.Done()

	var lastEvent int64
	failures := 0
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()

	for {
		d, err := cl.Deployment(ctx, id)
		switch {
		case err == nil:
			failures = 0
		case ctx.Err() != nil:
			return api.DeploymentDetail{}, ctx.Err()
		default:
			// Only connectivity problems are worth riding out.
			var unreachable *client.UnreachableError
			if failures++; !errors.As(err, &unreachable) || failures >= maxPollFailures {
				return api.DeploymentDetail{}, err
			}
			c.ui.Progress("Waiting for the agent to respond")
		}

		if err == nil {
			for _, e := range d.Events {
				if e.ID <= lastEvent {
					continue
				}
				lastEvent = e.ID
				c.echoEvent(e, static)
			}
			if d.CompletedAt != nil {
				return d, nil
			}
		}

		select {
		case <-ticker.C:
		case <-ctx.Done():
			return api.DeploymentDetail{}, ctx.Err()
		}
	}
}

// progressLabels say what the agent is busy with in each state.
var progressLabels = map[api.DeploymentStatus]string{
	api.StatusBuilding:       "Pulling image",
	api.StatusStarting:       "Starting containers",
	api.StatusHealthChecking: "Checking that the new version is healthy",
	api.StatusHealthy:        "Switching over",
	api.StatusActive:         "Retiring the previous version",
}

func (c *cli) echoEvent(e api.Event, static bool) {
	switch {
	case e.Type == api.EventStep && e.Level == api.LevelWarn:
		c.ui.Warn("%s", e.Message)
	case e.Type == api.EventStep:
		c.ui.Success("%s", e.Message)
	case e.Type == api.EventState:
		if label, ok := progressLabel(api.DeploymentStatus(e.Message), static); ok {
			c.ui.Progress("%s", label)
		}
	}
	// Failure and log events are rendered by reportFailedDeployment.
}

func (c *cli) reportFailedDeployment(ctx context.Context, cl *client.Client, d api.DeploymentDetail) {
	if d.Status == api.StatusRolledBack {
		c.ui.Failure("Deployment failed and was rolled back")
	} else {
		c.ui.Failure("Deployment failed")
	}
	c.ui.Println()
	c.ui.Println("  " + d.Error)
	for _, e := range d.Events {
		if e.Type != api.EventLog {
			continue
		}
		c.ui.Println()
		for _, line := range strings.Split(e.Message, "\n") {
			c.ui.Println("  " + c.ui.Styled(ui.Dim, line))
		}
	}
	c.ui.Println()

	// The most useful thing to know after a failure: are my users affected?
	detail, err := cl.Application(ctx, d.Application)
	switch {
	case err != nil:
		return
	case detail.ActiveDeployment != nil && detail.Status != api.AppHealthy && detail.Status != api.AppStopped:
		// Say what is true now rather than what usually is: a rollback can
		// fail too, and then "did not affect it" would be a lie.
		c.ui.Printf("%s is running %s, but it is %s right now (%d/%d replicas healthy). Shipwick keeps trying to restore it:\n  shipwick status %s\n",
			d.Application, detail.ActiveDeployment.Version, detail.Status, detail.Replicas.Healthy, detail.Replicas.Desired, d.Application)
	case detail.ActiveDeployment != nil && d.Status == api.StatusRolledBack:
		// Part of the old version had already been replaced when the new one
		// failed; it was restored. Capacity may have dipped, service did not stop.
		c.ui.Printf("%s is running %s again: the previous version was restored.\n",
			d.Application, detail.ActiveDeployment.Version)
	case detail.ActiveDeployment != nil:
		c.ui.Printf("%s is still running %s; the failed deployment did not affect it.\n",
			d.Application, detail.ActiveDeployment.Version)
	default:
		c.ui.Printf("%s has no running version.\n", d.Application)
	}
}

// overrideImage sets the top-level `image` key of a deploy.yaml document.
// Editing the YAML tree, rather than re-serializing the parsed spec, keeps
// what is sent to the agent a plain deploy.yaml with everything else intact.
// Input that is not a YAML mapping is returned untouched for Parse to reject.
func overrideImage(data []byte, image string) []byte {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return data
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return data
	}
	root := doc.Content[0]
	value := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: image}

	replaced := false
	for i := 0; i+1 < len(root.Content); i += 2 {
		if root.Content[i].Value == "image" {
			root.Content[i+1] = value
			replaced = true
		}
	}
	if !replaced {
		root.Content = append(root.Content, &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: "image"}, value)
	}

	out, err := yaml.Marshal(&doc)
	if err != nil {
		return data
	}
	return out
}

func (c *cli) validateCommand() *cobra.Command {
	var files, envFiles []string
	cmd := &cobra.Command{
		Use:   "validate [name...]",
		Short: "Check deploy.yaml without deploying",
		Long: `Check deploy.yaml without deploying: it is read, its ${NAME} placeholders are
filled in from the environment and --env-file, and it is validated exactly as
the agent would validate it. Env values whose name is set nowhere here are
listed: the server fills them in from its secrets when you deploy. A
shipwick.yaml is checked the same way, entry by entry, and the order the
applications deploy in is shown. With names of its applications, the whole
file is checked and those are shown, as "shipwick deploy" with the same names
would deploy them.`,
		Args: cobra.ArbitraryArgs,
		RunE: func(cmd *cobra.Command, names []string) error {
			if handled, err := c.validateMany(cmd, files, envFiles, names); handled {
				return err
			}
			if len(names) > 0 {
				return refuseNames(cmd, files)
			}
			for i, file := range files {
				_, app, vars, err := c.loadConfig(file, envFiles, "")
				if err != nil {
					return err
				}
				if i > 0 {
					c.ui.Println()
				}
				c.ui.Success("%s is valid%s", file, substitutedNote(vars))
				c.ui.Println()
				c.ui.Fields(describeSpec(app))
				c.printDeferred(vars)
				c.noteBuild(app)
			}
			return nil
		},
	}
	filesFlag(cmd, &files, &envFiles)
	return cmd
}

// describeSpec summarizes a spec as it will be applied, defaults included.
func describeSpec(app spec.App) [][2]string {
	fields := [][2]string{{"Name", app.Name}}
	if app.Static != nil {
		fields = append(fields, [2]string{"Folder", app.Static.Dir + "/ — served by the proxy, no container"})
		if app.Static.Fallback != "" {
			fields = append(fields, [2]string{"Fallback", app.Static.Fallback + " for paths that name no file"})
		}
	} else {
		fields = append(fields, describeImage(app)...)
		fields = append(fields, [2]string{"Replicas", fmt.Sprint(app.Replicas)})
	}
	if app.Port != 0 {
		fields = append(fields, [2]string{"Port", fmt.Sprint(app.Port)})
	}
	if app.Domain != "" {
		fields = append(fields, [2]string{"Domain", app.Domain})
	}
	if app.Path != "" {
		fields = append(fields, [2]string{"Path", app.Path})
	}
	if len(app.Aliases) > 0 {
		fields = append(fields, [2]string{"Aliases", strings.Join(app.Aliases, ", ")})
	}
	if len(app.Redirects) > 0 {
		fields = append(fields, [2]string{"Redirects", strings.Join(app.Redirects, ", ") + " → https://" + app.Domain})
	}
	if app.Proxy != nil {
		fields = append(fields, [2]string{"Proxy", describeProxy(app)})
	}
	if h := app.Health; h != nil {
		line := fmt.Sprintf("%s every %s (timeout %s, %d retries)", describeHealthCheck(*h), h.Interval, h.Timeout, h.Retries)
		if h.StartPeriod > 0 {
			line += fmt.Sprintf(", after a %s start period", h.StartPeriod)
		}
		fields = append(fields, [2]string{"Health check", line})
	}
	if app.Static == nil {
		fields = append(fields, [2]string{"Resources", describeResources(app.Resources)})
	}
	for _, v := range app.Volumes {
		fields = append(fields, [2]string{"Volume", v.Name + " at " + v.Path})
	}
	for _, p := range app.Publish {
		fields = append(fields, [2]string{"Publish", describePublish(p)})
	}
	if app.Static == nil {
		fields = append(fields, [2]string{"Restart", app.Restart.Policy})
	}
	if app.Deploy.Strategy != spec.StrategyRolling {
		fields = append(fields, [2]string{"Strategy", app.Deploy.Strategy})
	}
	if len(app.Env) > 0 {
		fields = append(fields, [2]string{"Environment", fmt.Sprintf("%d variables", len(app.Env))})
	}
	if len(app.Entrypoint) > 0 {
		fields = append(fields, [2]string{"Entrypoint", describeArgv(app.Entrypoint)})
	}
	if len(app.Command) > 0 {
		fields = append(fields, [2]string{"Command", describeArgv(app.Command)})
	}
	if app.User != "" {
		fields = append(fields, [2]string{"User", app.User})
	}
	if app.Security != nil {
		fields = append(fields, [2]string{"Security", describeSecurity(*app.Security)})
	}
	if app.Logging != nil {
		fields = append(fields, [2]string{"Logging", describeLogging(*app.Logging)})
	}
	if app.PreDeploy != nil {
		fields = append(fields, [2]string{"Pre-deploy", strings.Join(app.PreDeploy.Command, " ")})
	}
	for _, j := range app.Jobs {
		fields = append(fields, [2]string{"Job", fmt.Sprintf("%s at %s UTC: %s", j.Name, j.Schedule, strings.Join(j.Command, " "))})
	}
	if app.Backups != nil {
		fields = append(fields, [2]string{"Backups", describeBackupPlan(*app.Backups)})
	}
	return fields
}

// describePublish reads "5432/tcp → server port 15432 on 10.0.0.5".
func describePublish(p spec.Publish) string {
	s := fmt.Sprintf("%d/%s → server port %d", p.Port, p.Protocol, p.Host)
	if p.Address != "" {
		s += " on " + p.Address
	}
	return s
}

func describeResources(r spec.Resources) string {
	cpu, mem := "unlimited CPU", "unlimited memory"
	if r.CPU > 0 {
		cpu = fmt.Sprintf("%g CPU", r.CPU)
	}
	if r.MemoryBytes > 0 {
		mem = spec.FormatMemory(r.MemoryBytes)
	}
	return cpu + ", " + mem
}

// describeHealthCheck names the check itself: "GET /health", "TCP :5432" or
// the command.
func describeHealthCheck(h spec.Health) string {
	switch h.Kind() {
	case spec.HealthTCP:
		return fmt.Sprintf("TCP :%d", h.TCP)
	case spec.HealthCommand:
		return "command " + strings.Join(h.Command, " ")
	}
	return "GET " + h.Path
}
