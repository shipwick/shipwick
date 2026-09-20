package commands

import (
	"context"
	"fmt"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const (
	recentDeployments = 5
	recentEvents      = 8
)

func (c *cli) statusCommand() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "status [app]",
		Short: "Show the state of an application",
		Long: `Show the state of an application: its version, replicas and recent deployments.

Without an argument, the application described by deploy.yaml is shown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := resolveApp(args, file)
			if err != nil {
				return err
			}
			return c.status(cmd.Context(), name)
		},
	}
	fileFlag(cmd, &file)
	return cmd
}

func (c *cli) status(ctx context.Context, name string) error {
	cl, err := c.connect()
	if err != nil {
		return err
	}
	app, err := cl.Application(ctx, name)
	if err != nil {
		return err
	}
	history, err := cl.Deployments(ctx, name, recentDeployments)
	if err != nil {
		return err
	}
	now := c.now()

	c.ui.Println(c.ui.Styled(ui.Bold, app.Name) + "  " + c.appStatus(app.Application))
	c.ui.Println()

	fields := [][2]string{}
	if d := app.ActiveDeployment; d != nil {
		deployed := d.StartedAt
		if d.CompletedAt != nil {
			deployed = *d.CompletedAt
		}
		fields = append(fields,
			[2]string{"Version", fmt.Sprintf("%s  %s", d.Version, c.ui.Styled(ui.Dim, fmt.Sprintf("(deployment #%d, %s)", d.Sequence, ui.RelativeTime(deployed, now))))},
			[2]string{"Image", d.Image},
		)
	}
	if app.Domain != "" {
		fields = append(fields, [2]string{"URL", "https://" + app.Domain})
	}
	fields = append(fields, [2]string{"Replicas", fmt.Sprintf("%d/%d healthy", app.Replicas.Healthy, app.Replicas.Desired)})
	// Usage is a nicety here: an agent too old to report it, or an application
	// with nothing running, simply has no such lines.
	if m, err := cl.Metrics(ctx, name); err == nil && app.Replicas.Running > 0 {
		fields = append(fields,
			[2]string{"CPU", formatCPUUsage(m.CPUPercent, m.CPULimitPercent)},
			[2]string{"Memory", formatMemoryUsage(m.MemoryBytes, m.MemoryLimitBytes)},
		)
	}
	if app.Spec != nil {
		if h := app.Spec.Health; h != nil {
			fields = append(fields, [2]string{"Health", fmt.Sprintf("GET %s every %s", h.Path, h.Interval)})
		}
		fields = append(fields, [2]string{"Limits", describeResources(app.Spec.Resources)})
	}
	c.ui.Fields(fields)

	if len(app.Containers) > 0 {
		c.ui.Println()
		rows := make([][]ui.Cell, 0, len(app.Containers))
		for _, ct := range app.Containers {
			rows = append(rows, []ui.Cell{
				ui.C(fmt.Sprint(ct.Replica)),
				ui.C(ct.Name),
				c.containerState(ct),
				containerHealth(ct),
				containerRestarts(ct),
				ui.C(startedAgo(ct, now)),
			})
		}
		c.ui.Table([]string{"REPLICA", "CONTAINER", "STATE", "HEALTH", "RESTARTS", "STARTED"}, rows)
	}

	if len(history) > 0 {
		c.ui.Println()
		rows := make([][]ui.Cell, 0, len(history))
		for _, d := range history {
			rows = append(rows, []ui.Cell{
				ui.C(fmt.Sprintf("#%d", d.Sequence)),
				ui.C(d.Version),
				{Text: string(d.Status), Style: deploymentStyle(d.Status)},
				{Text: d.Kind, Style: ui.Dim},
				ui.C(ui.RelativeTime(d.StartedAt, now)),
				{Text: truncate(d.Error, 90), Style: ui.Dim},
			})
		}
		c.ui.Table([]string{"DEPLOY", "VERSION", "STATUS", "VIA", "WHEN", ""}, rows)
	}

	// What the supervisor has been doing: the difference between "it is
	// healthy" and "it is healthy now, after restarting four times tonight".
	events, err := cl.Events(ctx, name, recentEvents)
	if err != nil {
		return err
	}
	if len(events) > 0 {
		c.ui.Println()
		rows := make([][]ui.Cell, 0, len(events))
		for _, e := range events {
			rows = append(rows, []ui.Cell{
				ui.C(ui.RelativeTime(e.CreatedAt, now)),
				{Text: e.Message, Style: eventStyle(e.Level)},
			})
		}
		c.ui.Table([]string{"WHEN", "EVENT"}, rows)
	}
	return nil
}

func eventStyle(level string) ui.Style {
	switch level {
	case api.LevelError:
		return ui.Red
	case api.LevelWarn:
		return ui.Yellow
	}
	return ui.Plain
}

func containerHealth(ct api.Container) ui.Cell {
	switch ct.Health {
	case api.HealthHealthy:
		return ui.Cell{Text: "healthy", Style: ui.Green}
	case api.HealthUnhealthy:
		return ui.Cell{Text: "unhealthy", Style: ui.Red}
	case api.HealthStarting:
		return ui.Cell{Text: "starting", Style: ui.Yellow}
	case api.HealthUnknown:
		return ui.Cell{Text: "checking", Style: ui.Dim}
	}
	return ui.C("") // no health check configured
}

func containerRestarts(ct api.Container) ui.Cell {
	switch {
	case ct.CrashLoop:
		return ui.Cell{Text: fmt.Sprintf("%d (crash loop)", ct.Restarts), Style: ui.Red}
	case ct.Restarts > 0:
		return ui.Cell{Text: fmt.Sprint(ct.Restarts), Style: ui.Yellow}
	}
	return ui.C("0")
}

func (c *cli) psCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "ps",
		Short: "List the applications on the server",
		Args:  cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			apps, err := cl.Applications(cmd.Context())
			if err != nil {
				return err
			}
			if len(apps) == 0 {
				c.ui.Println("No applications yet. Deploy one with: deployctl deploy")
				return nil
			}

			now := c.now()
			rows := make([][]ui.Cell, 0, len(apps))
			for _, app := range apps {
				status := string(app.Status)
				if app.Deploying {
					status += " (deploying)"
				}
				rows = append(rows, []ui.Cell{
					ui.C(app.Name),
					{Text: status, Style: appStyle(app.Status)},
					ui.C(app.Version),
					ui.C(fmt.Sprintf("%d/%d", app.Replicas.Healthy, app.Replicas.Desired)),
					ui.C(app.Domain),
					ui.C(ui.RelativeTime(app.UpdatedAt, now)),
				})
			}
			c.ui.Table([]string{"NAME", "STATUS", "VERSION", "REPLICAS", "DOMAIN", "UPDATED"}, rows)
			return nil
		},
	}
}

func (c *cli) appStatus(app api.Application) string {
	s := c.ui.Styled(appStyle(app.Status), "● "+string(app.Status))
	if app.Deploying {
		s += c.ui.Styled(ui.Dim, "  (deployment in progress)")
	}
	return s
}

func appStyle(s api.ApplicationStatus) ui.Style {
	switch s {
	case api.AppHealthy:
		return ui.Green
	case api.AppDegraded, api.AppDeploying:
		return ui.Yellow
	case api.AppDown, api.AppFailed, api.AppCrashLoop:
		return ui.Red
	}
	return ui.Dim // STOPPED
}

func deploymentStyle(s api.DeploymentStatus) ui.Style {
	switch s {
	case api.StatusActive:
		return ui.Green
	case api.StatusFailed:
		return ui.Red
	case api.StatusSuperseded:
		return ui.Dim
	}
	return ui.Yellow // in flight, or rolled back: failed, but handled
}

func (c *cli) containerState(ct api.Container) ui.Cell {
	switch {
	case ct.State == "running":
		return ui.Cell{Text: "running", Style: ui.Green}
	case ct.OOMKilled:
		return ui.Cell{Text: "out of memory", Style: ui.Red}
	case ct.State == "exited" && ct.ExitCode == 0:
		return ui.Cell{Text: "exited (0)", Style: ui.Dim} // a clean stop, e.g. `deployctl stop`
	case ct.State == "exited":
		return ui.Cell{Text: fmt.Sprintf("exited (%d)", ct.ExitCode), Style: ui.Red}
	}
	return ui.Cell{Text: ct.State, Style: ui.Yellow}
}

func startedAgo(ct api.Container, now time.Time) string {
	if ct.State != "running" || ct.StartedAt == nil {
		return ""
	}
	return ui.RelativeTime(*ct.StartedAt, now)
}

// truncate shortens s to at most max characters for a table cell. The full
// text is one `deployctl deploy` away, or in GET /deployments/:id.
func truncate(s string, max int) string {
	r := []rune(s)
	if len(r) <= max {
		return s
	}
	return string(r[:max-1]) + "…"
}

// formatCPU renders CPU usage in percent of one core: "42%", "0.3%", "215%".
func formatCPU(percent float64) string {
	if percent > 0 && percent < 10 {
		return fmt.Sprintf("%.1f%%", percent)
	}
	return fmt.Sprintf("%.0f%%", percent)
}

// formatMemoryUsage renders "412 MB / 1 GB", or just the usage when unlimited.
func formatMemoryUsage(used, limit int64) string {
	if limit <= 0 {
		return spec.FormatMemory(used)
	}
	return spec.FormatMemory(used) + " / " + spec.FormatMemory(limit)
}

// formatCPUUsage renders "42% / 200%", or just the usage when unlimited.
// Percent of one core on both sides: a limit of `cpu: 2` reads 200%.
func formatCPUUsage(used, limit float64) string {
	if limit <= 0 {
		return formatCPU(used)
	}
	return formatCPU(used) + " / " + formatCPU(limit)
}
