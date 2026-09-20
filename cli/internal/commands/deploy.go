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
	var file, image string
	var noWait bool

	cmd := &cobra.Command{
		Use:   "deploy",
		Short: "Deploy the application described by deploy.yaml",
		Long: `Deploy the application described by deploy.yaml and wait for the result.

Replicas are replaced one at a time, each new one only after it proved healthy,
so the application keeps serving throughout. If the new version fails, the
deployment is undone — replicas already replaced are restored — and the
command exits non-zero.

In CI, keep deploy.yaml in the repository and supply the freshly built image:

  deployctl deploy --image ghcr.io/company/my-api:$GIT_SHA`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.deploy(cmd.Context(), file, image, noWait)
		},
	}
	fileFlag(cmd, &file)
	cmd.Flags().StringVar(&image, "image", "", "deploy this image instead of the one in the config")
	cmd.Flags().BoolVar(&noWait, "no-wait", false, "start the deployment and return immediately")
	return cmd
}

func (c *cli) deploy(ctx context.Context, file, image string, noWait bool) error {
	data, err := readFile(file)
	if err != nil {
		return err
	}
	if image != "" {
		data = overrideImage(data, image)
	}
	app, err := spec.Parse(data)
	if err != nil {
		return err
	}

	cl, err := c.connect()
	if err != nil {
		return err
	}

	c.ui.Println("Deploying " + c.ui.Styled(ui.Bold, app.Name) + "...")
	c.ui.Println()
	c.ui.Success("Validated %s", file)

	started := c.now()
	d, err := cl.Deploy(ctx, app.Name, data)
	if err != nil {
		return err
	}
	return c.followDeployment(ctx, cl, d, started, noWait)
}

// followDeployment is the second half of every command that starts a
// deployment — deploy, redeploy, rollback: wait for it, narrate it, report
// the outcome, and turn a failure into a non-zero exit.
func (c *cli) followDeployment(ctx context.Context, cl *client.Client, d api.Deployment, started time.Time, noWait bool) error {
	name := d.Application
	if noWait {
		c.ui.Success("Deployment #%d started", d.Sequence)
		c.ui.Println()
		c.ui.Println("Follow it with: deployctl status " + name)
		return nil
	}

	final, err := c.awaitDeployment(ctx, cl, d.ID)
	if err != nil {
		if ctx.Err() != nil {
			c.ui.Println()
			c.ui.Println("Stopped waiting. The deployment continues on the server:")
			c.ui.Println("  deployctl status " + name)
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
		c.ui.Printf("%d/%d replicas healthy\n", detail.Replicas.Healthy, detail.Replicas.Desired)
	}
	if final.Spec.Domain != "" {
		c.ui.Println("https://" + final.Spec.Domain)
	}
	return nil
}

// awaitDeployment polls until the agent is done with the deployment, echoing
// its events as they appear. "Done" is completed_at, not the first settled
// status: until then the application still refuses new operations.
func (c *cli) awaitDeployment(ctx context.Context, cl *client.Client, id int64) (api.DeploymentDetail, error) {
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
				c.echoEvent(e)
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

func (c *cli) echoEvent(e api.Event) {
	switch {
	case e.Type == api.EventStep && e.Level == api.LevelWarn:
		c.ui.Warn("%s", e.Message)
	case e.Type == api.EventStep:
		c.ui.Success("%s", e.Message)
	case e.Type == api.EventState:
		if label, ok := progressLabels[api.DeploymentStatus(e.Message)]; ok {
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
		c.ui.Printf("%s is running %s, but it is %s right now (%d/%d replicas healthy). Shipwick keeps trying to restore it:\n  deployctl status %s\n",
			d.Application, detail.ActiveDeployment.Version, detail.Status, detail.Replicas.Healthy, detail.Replicas.Desired, d.Application)
	case detail.ActiveDeployment != nil && d.Status == api.StatusRolledBack:
		// Part of the old version had already been replaced when the new one
		// failed; it was restored. Capacity may have dipped, service did not stop.
		c.ui.Printf("%s is running %s again: the replicas that had already been replaced were restored.\n",
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
	var file string
	cmd := &cobra.Command{
		Use:   "validate",
		Short: "Check deploy.yaml without deploying",
		Args:  cobra.NoArgs,
		RunE: func(*cobra.Command, []string) error {
			_, app, err := readConfig(file)
			if err != nil {
				return err
			}
			c.ui.Success("%s is valid", file)
			c.ui.Println()
			c.ui.Fields(describeSpec(app))
			return nil
		},
	}
	fileFlag(cmd, &file)
	return cmd
}

// describeSpec summarizes a spec as it will be applied, defaults included.
func describeSpec(app spec.App) [][2]string {
	fields := [][2]string{
		{"Name", app.Name},
		{"Image", app.Image},
		{"Version", app.Version()},
		{"Replicas", fmt.Sprint(app.Replicas)},
	}
	if app.Port != 0 {
		fields = append(fields, [2]string{"Port", fmt.Sprint(app.Port)})
	}
	if app.Domain != "" {
		fields = append(fields, [2]string{"Domain", app.Domain})
	}
	if app.Health != nil {
		fields = append(fields, [2]string{"Health check", fmt.Sprintf("GET %s every %s (timeout %s, %d retries)",
			app.Health.Path, app.Health.Interval, app.Health.Timeout, app.Health.Retries)})
	}
	fields = append(fields, [2]string{"Resources", describeResources(app.Resources)})
	fields = append(fields, [2]string{"Restart", app.Restart.Policy})
	if len(app.Env) > 0 {
		fields = append(fields, [2]string{"Environment", fmt.Sprintf("%d variables", len(app.Env))})
	}
	return fields
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
