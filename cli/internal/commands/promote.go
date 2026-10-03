package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

// A promotion runs on the server and keeps a record there; this command asks
// for one and then reads the record until it is done. Nothing of the
// promotion depends on the command: what it loses with its connection is
// only its place in the record, which it finds again.

// promotionGoesOn is said whenever the command stops following before the
// promotion has ended.
const promotionGoesOn = "The promotion goes on without this command, across a restart of the agent as well. Follow it with: shipwick standby promote"

// away reports whether err says that the agent could not be asked just now:
// it is restarting, the proxy answers in its place, or it is shutting down.
func away(err error) bool {
	var unreachable *client.UnreachableError
	var apiErr *client.APIError
	return errors.As(err, &unreachable) || (errors.As(err, &apiErr) && apiErr.Status == http.StatusServiceUnavailable)
}

func (c *cli) promote(ctx context.Context, cl *client.Client, yes bool) error {
	server := c.describeServer(cl.URL())

	// The promotion that ran last tells this one apart from it later; one
	// that is running is followed, whoever began it.
	last, err := cl.Promotion(ctx)
	switch {
	case client.IsCode(err, api.CodeEndpointNotFound):
		return c.promoteHeld(ctx, cl, yes)
	case client.IsCode(err, api.CodeNotFound):
	case err != nil:
		return err
	case last.CompletedAt == nil:
		c.ui.Println("A promotion is running on " + server + ". Following it.")
		return c.followPromotion(ctx, cl, last)
	}

	names, err := c.confirmPromotion(ctx, cl, yes)
	if err != nil || len(names) == 0 {
		return err
	}
	c.ui.Progress("Starting %s", strings.Join(names, ", "))
	promotion, err := c.startPromotion(ctx, cl, last.ID)
	if err != nil {
		c.ui.Done()
		return c.stoppedWaiting(ctx, err, promotionGoesOn)
	}
	return c.followPromotion(ctx, cl, promotion)
}

// confirmPromotion lists what a promotion would start and asks for it. It
// returns no names when nothing waits or the answer is no.
func (c *cli) confirmPromotion(ctx context.Context, cl *client.Client, yes bool) ([]string, error) {
	standby, err := cl.Standby(ctx)
	if err != nil {
		return nil, err
	}
	if len(standby.Applications) == 0 {
		c.ui.Println("No application is waiting for a promotion on " + c.describeServer(cl.URL()) + ".")
		return nil, nil
	}
	names := make([]string, 0, len(standby.Applications))
	for _, a := range standby.Applications {
		names = append(names, a.Name)
	}
	if !yes {
		if !isTerminal(c.in) {
			return nil, errors.New("refusing to promote without confirmation; pass --yes")
		}
		c.ui.Printf("This starts %s on %s: %s.\nThe server they were exported from must no longer be serving.\nType promote to confirm: ",
			plural(len(names), "application"), c.describeServer(cl.URL()), strings.Join(names, ", "))
		answer, _ := bufio.NewReader(c.in).ReadString('\n')
		if strings.TrimSpace(answer) != "promote" {
			return nil, errors.New("cancelled")
		}
	}
	return names, nil
}

// startPromotion asks for the promotion until the agent has answered. A
// request whose answer was lost may have arrived: the promotion the server
// then has is a later one than `last`, and is the one to follow.
func (c *cli) startPromotion(ctx context.Context, cl *client.Client, last int64) (api.Promotion, error) {
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for failures := 0; ; {
		promotion, err := cl.StartPromotion(ctx)
		switch {
		case err == nil:
			return promotion, nil
		case ctx.Err() != nil:
			return api.Promotion{}, ctx.Err()
		case client.IsCode(err, api.CodePromotionInProgress):
			// Begun by the request whose answer was lost, or by somebody
			// else in this moment: one promotion either way.
			return cl.Promotion(ctx)
		case !away(err):
			return api.Promotion{}, err
		}
		if failures++; failures >= maxPollFailures {
			return api.Promotion{}, err
		}
		c.ui.Progress("Waiting for the agent to respond")
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return api.Promotion{}, ctx.Err()
		}
		if current, err := cl.Promotion(ctx); err == nil && current.ID > last {
			return current, nil
		}
	}
}

// followPromotion reads the promotion until it has ended, says what became
// of each application as that is known, and prints the DNS records.
func (c *cli) followPromotion(ctx context.Context, cl *client.Client, promotion api.Promotion) error {
	said := map[string]bool{}
	failures := 0
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		c.echoPromotion(promotion, said)
		if promotion.CompletedAt != nil {
			break
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			c.ui.Done()
			return c.stoppedWaiting(ctx, ctx.Err(), promotionGoesOn)
		}
		next, err := cl.Promotion(ctx)
		switch {
		case err == nil:
			failures, promotion = 0, next
		case ctx.Err() != nil:
			c.ui.Done()
			return c.stoppedWaiting(ctx, ctx.Err(), promotionGoesOn)
		default:
			// Only an agent that is away is worth waiting for.
			if failures++; !away(err) || failures >= maxPollFailures {
				c.ui.Done()
				return fmt.Errorf("%w\n\n%s", err, promotionGoesOn)
			}
			c.ui.Progress("Waiting for the agent to respond")
		}
	}
	c.ui.Done()

	if len(promotion.Applications) == 0 {
		c.ui.Println("No application was waiting for a promotion any more.")
		return nil
	}
	c.printRecords(promotion.Records)
	if promotion.Status == api.PromotionFailed {
		return ErrReported
	}
	return nil
}

// echoPromotion prints the applications whose outcome is known and was not
// said yet, and names the one the promotion is at.
func (c *cli) echoPromotion(promotion api.Promotion, said map[string]bool) {
	for i, a := range promotion.Applications {
		switch a.Status {
		case api.PromotedPending:
		case api.PromotedStarting:
			c.ui.Progress("Starting %s (%d of %d)", a.Name, i+1, len(promotion.Applications))
		default:
			if !said[a.Name] {
				said[a.Name] = true
				c.printPromoted(a)
			}
		}
	}
}

func (c *cli) printPromoted(a api.PromotedApplication) {
	switch a.Status {
	case api.PromotedRunning:
		c.ui.Success("%s is running", a.Name)
	case api.PromotedStarted:
		c.ui.Warn("%s: %s", a.Name, a.Message)
	default:
		c.ui.Failure("%s could not be started: %s", a.Name, a.Message)
	}
}

// promoteHeld is the promotion against an agent that keeps no record of one:
// a single request that is held until every application has started.
func (c *cli) promoteHeld(ctx context.Context, cl *client.Client, yes bool) error {
	names, err := c.confirmPromotion(ctx, cl, yes)
	if err != nil || len(names) == 0 {
		return err
	}
	c.ui.Progress("Starting %s", strings.Join(names, ", "))
	promotion, err := cl.Promote(ctx)
	c.ui.Done()
	if err != nil {
		var unreachable *client.UnreachableError
		if errors.As(err, &unreachable) {
			err = fmt.Errorf("%w\n\nThe agent is older than this shipwick and answers a promotion only when it has ended, on the connection that was lost. The promotion itself goes on", err)
		}
		return c.stoppedWaiting(ctx, err, "Applications that were started keep running; see them with: shipwick ps")
	}
	failed := false
	for _, a := range promotion.Applications {
		c.printPromoted(a)
		failed = failed || (a.Status != api.PromotedRunning && a.Status != api.PromotedStarted)
	}
	c.printRecords(promotion.Records)
	if failed {
		return ErrReported
	}
	return nil
}

// describePromotion is the promotion's part of `shipwick standby`.
func (c *cli) describePromotion(p api.Promotion) {
	if p.CompletedAt == nil {
		c.ui.Println("A promotion is running, begun " + ui.RelativeTime(p.StartedAt, c.now()) + ". Follow it with: shipwick standby promote")
	} else {
		c.ui.Println(fmt.Sprintf("Promoted %s: %s started.", ui.RelativeTime(*p.CompletedAt, c.now()), plural(len(p.Applications), "application")))
	}
	for _, a := range p.Applications {
		if a.Status == api.PromotedFailed || a.Status == api.PromotedStarted || p.CompletedAt == nil {
			line := "  " + a.Name + ": " + a.Status
			if a.Message != "" && a.Status == api.PromotedFailed {
				line += ": " + a.Message
			}
			c.ui.Println(line)
		}
	}
	c.ui.Println()
}
