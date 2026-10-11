package commands

import (
	"context"
	"net/http"
	"strings"
	"time"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/version"
)

// After a deployment the agent asks the application its own hostname once,
// through the proxy, and records what it found as a `probe` event of the
// deployment — after completed_at, so `deploy` has its summary printed by
// then. The event is worth the wait: a redirect loop is the one thing a
// successful deployment can hide, and the deployment's output is where it is
// looked for.

// probePolls is how many polls `deploy` gives the agent's look after the
// summary: six seconds at the usual pace, a request's limit and some.
const probePolls = 12

// probesAfterDeploy reports whether an agent of this version asks the
// application through the proxy after a deployment: 0.9 and later, and a
// development build.
func probesAfterDeploy(agentVersion string) bool {
	return !parses(agentVersion) || version.Compare(agentVersion, "0.9.0") >= 0
}

// probeEvent finds the agent's look among a deployment's events.
func probeEvent(events []api.Event) (api.Event, bool) {
	for _, e := range events {
		if e.Type == api.EventProbe {
			return e, true
		}
	}
	return api.Event{}, false
}

// routed reports whether the deployment put its hostname in the proxy: only
// then is there anything to ask through it.
func routed(events []api.Event) bool {
	for _, e := range events {
		if e.Type == api.EventStep && strings.HasPrefix(e.Message, "Routed https://") {
			return true
		}
	}
	return false
}

// reportProbe prints what the agent found at the application's hostname,
// waiting a little for it when the agent is still asking. An agent that does
// not ask — older than 0.9 — costs one request and no wait.
func (c *cli) reportProbe(ctx context.Context, cl *client.Client, final api.DeploymentDetail) {
	if final.Spec.Domain == "" || final.Spec.Static != nil || !routed(final.Events) {
		return
	}
	if e, ok := probeEvent(final.Events); ok {
		c.printProbe(e)
		return
	}
	health, err := cl.Health(ctx)
	if err != nil || !probesAfterDeploy(health.Version) {
		return
	}
	address := "https://" + final.Spec.Domain + final.Spec.Path + "/"
	c.ui.Progress("Asking %s through the proxy", address)
	defer c.ui.Done()
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for i := 0; i < probePolls; i++ {
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return
		}
		d, err := cl.Deployment(ctx, final.ID)
		if err != nil {
			return
		}
		if e, ok := probeEvent(d.Events); ok {
			c.printProbe(e)
			return
		}
	}
	c.ui.Note("The agent is still asking %s through the proxy; what it finds is shown by: shipwick status %s", address, final.Application)
}

func (c *cli) printProbe(e api.Event) {
	if e.Level == api.LevelWarn {
		c.ui.Warn("%s", e.Message)
		return
	}
	c.ui.Success("%s", e.Message)
}

// probeLines is `status`'s line about the active deployment's look through
// the proxy, when it found something to fix. An agent that did not look, or
// found nothing wrong, adds no line.
func (c *cli) probeLines(ctx context.Context, cl *client.Client, app api.ApplicationDetail) {
	if app.ActiveDeployment == nil || app.Domain == "" {
		return
	}
	d, err := cl.Deployment(ctx, app.ActiveDeployment.ID)
	if err != nil {
		return
	}
	e, ok := probeEvent(d.Events)
	if !ok || e.Level != api.LevelWarn {
		return
	}
	c.ui.Println()
	c.ui.Println(c.ui.Styled(ui.Yellow, "!") + " " + e.Message)
}

// redirectLoop is doctor's judgement of a redirect at the application's
// hostname: one that lands on a hostname of `redirects` is a loop, and a
// problem; any other redirect is the application's business and is reported
// as the answer it is.
func (r *report) redirectLoop(address string, resp *http.Response, app api.Application) bool {
	target, loop := api.RedirectLoop(resp.StatusCode, resp.Header.Get("Location"), app.Redirects)
	if !loop {
		return false
	}
	r.problem("%s sends its visitors to %s, which `redirects` sends back: every request loops. Make %s the `domain` and %s a redirect, or stop the application redirecting",
		address, target, target, app.Domain)
	return true
}
