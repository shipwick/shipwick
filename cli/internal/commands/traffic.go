package commands

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// trafficCommands are the commands that show what the proxy saw.
func (c *cli) trafficCommands() []*cobra.Command {
	return []*cobra.Command{c.trafficCommand()}
}

// trafficWindows are the values of --since: how many minutes each covers, and
// how it reads in a sentence.
var trafficWindows = map[string]struct {
	minutes int
	words   string
}{
	"1h":  {60, "the last hour"},
	"24h": {24 * 60, "the last 24 hours"},
	"7d":  {7 * 24 * 60, "the last 7 days"},
}

// requestsKept is how many requests the agent remembers per application.
const requestsKept = 200

func (c *cli) trafficCommand() *cobra.Command {
	var since string
	var requests, follow bool
	var tail int

	cmd := &cobra.Command{
		Use:   "traffic [app]",
		Short: "Show the requests the proxy served: rates, errors, latency",
		Long: `Show what the proxy saw: how many requests each application got, how many
failed, how long they took and how much was sent.

Without an application, every application is listed. With one, its totals,
and the slowest and the failing paths among its most recent requests.

  shipwick traffic
  shipwick traffic my-api --since 24h
  shipwick traffic my-api --requests       the most recent requests, one per line
  shipwick traffic my-api --requests -f    and every new one as it arrives

Counts are kept per minute for seven days. The requests themselves — the last
200 of each application, without query strings or headers — are kept in the
agent's memory only.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			window, ok := trafficWindows[since]
			if !ok {
				return errors.New("--since must be 1h, 24h or 7d")
			}
			if follow && !requests {
				return errors.New("--follow goes with --requests: shipwick traffic <app> --requests -f")
			}
			if tail < 1 || tail > requestsKept {
				return fmt.Errorf("--tail must be between 1 and %d: the agent keeps the last %d requests of an application", requestsKept, requestsKept)
			}
			if len(args) == 0 {
				if requests {
					return errors.New("--requests needs an application: shipwick traffic <app> --requests")
				}
				return c.trafficOverview(cmd.Context(), since, window.minutes, window.words)
			}
			name := args[0]
			if err := spec.ValidateName(name); err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			if requests {
				return c.trafficRequests(cmd.Context(), cl, name, tail, follow)
			}
			return c.trafficOf(cmd.Context(), cl, name, since, window.minutes, window.words)
		},
	}
	cmd.Flags().StringVar(&since, "since", "1h", "how far back to look: 1h, 24h or 7d")
	cmd.Flags().BoolVar(&requests, "requests", false, "list the most recent requests instead of the totals")
	cmd.Flags().BoolVarP(&follow, "follow", "f", false, "with --requests: keep printing new requests")
	cmd.Flags().IntVarP(&tail, "tail", "n", 50, "with --requests: how many to show")
	return cmd
}

// trafficOverview is one row per application.
func (c *cli) trafficOverview(ctx context.Context, since string, minutes int, words string) error {
	cl, err := c.connect()
	if err != nil {
		return err
	}
	apps, err := cl.Applications(ctx)
	if err != nil {
		return err
	}
	if len(apps) == 0 {
		c.ui.Println("No applications yet. Deploy one with: shipwick deploy")
		return nil
	}
	rows := make([][]ui.Cell, 0, len(apps))
	for _, app := range apps {
		t, err := cl.Traffic(ctx, app.Name, since)
		if err != nil {
			return err
		}
		rows = append(rows, []ui.Cell{
			ui.C(app.Name),
			ui.C(formatRate(t.Totals.Requests, minutes)),
			failedCell(t.Totals),
			ui.C(formatLatency(t.Totals, t.Totals.P95Ms)),
			ui.C(spec.FormatMemory(t.Totals.Bytes)),
		})
	}
	c.ui.Println(c.ui.Styled(ui.Dim, "Requests through the proxy over "+words))
	c.ui.Table([]string{"APP", "REQ/MIN", "5XX", "P95", "BYTES"}, rows)
	return nil
}

// trafficOf is one application: its totals, and what its recent requests say
// about where the time and the failures are.
func (c *cli) trafficOf(ctx context.Context, cl *client.Client, name, since string, minutes int, words string) error {
	t, err := cl.Traffic(ctx, name, since)
	if err != nil {
		return err
	}
	c.ui.Println(c.ui.Styled(ui.Bold, name) + "  " + c.ui.Styled(ui.Dim, "requests through the proxy over "+words))
	c.ui.Println()
	if t.Totals.Requests == 0 {
		c.ui.Println("No requests over " + words + ".")
		return nil
	}
	total := t.Totals
	failed := fmt.Sprintf("%d 5xx", total.Status5xx)
	if total.Status5xx > 0 {
		failed = c.ui.Styled(ui.Red, fmt.Sprintf("%d 5xx (%s)", total.Status5xx, formatShare(total.Status5xx, total.Requests)))
	}
	c.ui.Fields([][2]string{
		{"Requests", fmt.Sprintf("%d  %s", total.Requests, c.ui.Styled(ui.Dim, "("+formatRate(total.Requests, minutes)+"/min)"))},
		{"Status", fmt.Sprintf("%d 2xx · %d 3xx · %d 4xx · %s", total.Status2xx, total.Status3xx, total.Status4xx, failed)},
		{"Latency", fmt.Sprintf("p50 %s · p95 %s · p99 %s", formatMs(total.P50Ms), formatMs(total.P95Ms), formatMs(total.P99Ms))},
		{"Sent", spec.FormatMemory(total.Bytes)},
	})

	// Paths are only known for the requests the agent still remembers; the
	// counts above have none.
	recent, err := cl.Requests(ctx, name, requestsKept)
	if err != nil || len(recent) == 0 {
		return err
	}
	const shown = 5
	paths := summarizePaths(recent)

	sort.SliceStable(paths, func(i, j int) bool { return paths[i].slowest > paths[j].slowest })
	rows := [][]ui.Cell{}
	for _, p := range paths[:min(shown, len(paths))] {
		rows = append(rows, []ui.Cell{ui.C(truncate(p.path, 60)), ui.C(fmt.Sprint(p.requests)), ui.C(formatMs(p.slowest))})
	}
	c.ui.Println()
	c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("Slowest paths among the last %s", plural(len(recent), "request"))))
	c.ui.Table([]string{"PATH", "REQUESTS", "SLOWEST"}, rows)

	sort.SliceStable(paths, func(i, j int) bool { return paths[i].failed > paths[j].failed })
	rows = rows[:0]
	for _, p := range paths[:min(shown, len(paths))] {
		if p.failed == 0 {
			break
		}
		rows = append(rows, []ui.Cell{ui.C(truncate(p.path, 60)), {Text: fmt.Sprint(p.failed), Style: ui.Red}, ui.C(fmt.Sprint(p.lastFailure))})
	}
	if len(rows) > 0 {
		c.ui.Println()
		c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("Failing paths among the last %s", plural(len(recent), "request"))))
		c.ui.Table([]string{"PATH", "5XX", "LAST STATUS"}, rows)
	}
	return nil
}

type pathSummary struct {
	path        string
	requests    int
	slowest     float64 // milliseconds
	failed      int     // answered 5xx
	lastFailure int     // the status of the most recent one
}

// summarizePaths groups requests by path, in the order paths first appear.
func summarizePaths(requests []api.Request) []pathSummary {
	index := map[string]int{}
	var out []pathSummary
	for _, r := range requests {
		i, ok := index[r.Path]
		if !ok {
			i = len(out)
			index[r.Path] = i
			out = append(out, pathSummary{path: r.Path})
		}
		out[i].requests++
		out[i].slowest = max(out[i].slowest, r.DurationMs)
		if r.Status >= 500 {
			out[i].failed++
			out[i].lastFailure = r.Status
		}
	}
	return out
}

// trafficRequests prints the most recent requests and, when following, every
// new one. The agent keeps them in memory and is asked again every other
// second: a list of two hundred requests is not worth a stream.
func (c *cli) trafficRequests(ctx context.Context, cl *client.Client, name string, tail int, follow bool) error {
	requests, err := cl.Requests(ctx, name, tail)
	if err != nil {
		return err
	}
	var last time.Time
	for _, r := range requests {
		c.printRequest(r)
		last = r.Time
	}
	if !follow {
		if len(requests) == 0 {
			c.ui.Println("No requests yet since the agent started.")
		}
		return nil
	}
	for {
		select {
		case <-ctx.Done():
			return nil // Ctrl+C is the normal way out: no message
		case <-time.After(4 * c.pollInterval):
		}
		requests, err := cl.Requests(ctx, name, requestsKept)
		if err != nil {
			if ctx.Err() != nil {
				return nil
			}
			return err
		}
		fresh := 0
		for _, r := range requests {
			if r.Time.After(last) {
				c.printRequest(r)
				last = r.Time
				fresh++
			}
		}
		if fresh == requestsKept {
			c.ui.Warn("more requests arrived than the agent keeps (%d): some are not shown", requestsKept)
		}
	}
}

func (c *cli) printRequest(r api.Request) {
	status := ui.Green
	switch {
	case r.Status >= 500:
		status = ui.Red
	case r.Status >= 400:
		status = ui.Yellow
	case r.Status >= 300:
		status = ui.Cyan
	}
	c.ui.Println(fmt.Sprintf("%s  %s  %s %s  %s  %s  %s",
		c.ui.Styled(ui.Dim, r.Time.Local().Format("15:04:05")),
		c.ui.Styled(status, fmt.Sprint(r.Status)),
		r.Method, r.Path,
		formatMs(r.DurationMs),
		spec.FormatMemory(r.Bytes),
		c.ui.Styled(ui.Dim, r.Client)))
}

// certificateLines says, under an application's status, which of its
// hostnames have a certificate that is not in order, and why. In order is the
// normal case and is left out unless asked for.
func (c *cli) certificateLines(certs []api.HostnameCertificate, verbose bool) {
	rows := [][]ui.Cell{}
	for _, cert := range certs {
		switch cert.Status {
		case api.CertOK:
			if verbose {
				rows = append(rows, []ui.Cell{ui.C(cert.Hostname), {Text: describeValidity(cert), Style: ui.Green}})
			}
		case api.CertExpiring:
			text := cert.Message
			if cert.Issuer != "" {
				text += " (" + cert.Issuer + ")"
			}
			rows = append(rows, []ui.Cell{ui.C(cert.Hostname), {Text: text, Style: ui.Red}})
		case api.CertObtaining:
			rows = append(rows, []ui.Cell{ui.C(cert.Hostname), {Text: "being obtained: " + cert.Message, Style: ui.Yellow}})
		case api.CertWaitingForDNS:
			rows = append(rows, []ui.Cell{ui.C(cert.Hostname), {Text: "waiting for DNS: " + cert.Message, Style: ui.Yellow}})
		default:
			rows = append(rows, []ui.Cell{ui.C(cert.Hostname), {Text: "unknown: " + cert.Message, Style: ui.Dim}})
		}
	}
	if len(rows) == 0 {
		return
	}
	c.ui.Println()
	c.ui.Table([]string{"HOSTNAME", "CERTIFICATE"}, rows)
}

func describeValidity(cert api.HostnameCertificate) string {
	if cert.NotAfter == nil {
		return "valid"
	}
	text := "valid until " + cert.NotAfter.UTC().Format("2006-01-02")
	if cert.Issuer != "" {
		text += " (" + cert.Issuer + ")"
	}
	return text
}

// formatRate renders requests per minute: "0", "<0.1", "3.4", "208".
func formatRate(requests int64, minutes int) string {
	rate := float64(requests) / float64(minutes)
	switch {
	case requests == 0:
		return "0"
	case rate < 0.1:
		return "<0.1"
	case rate < 10:
		return fmt.Sprintf("%.1f", rate)
	}
	return fmt.Sprintf("%.0f", rate)
}

// formatMs renders a duration given in milliseconds: "0.6ms", "48ms", "1.2s".
func formatMs(ms float64) string {
	switch {
	case ms >= 1000:
		return fmt.Sprintf("%.1fs", ms/1000)
	case ms >= 10:
		return fmt.Sprintf("%.0fms", ms)
	}
	return fmt.Sprintf("%.1fms", ms)
}

// formatLatency is a percentile, or a dash when there was no request to
// measure.
func formatLatency(t api.TrafficCounts, ms float64) string {
	if t.Requests == 0 {
		return ""
	}
	return formatMs(ms)
}

// formatShare renders n as a percentage of total: "0.2%", "12%".
func formatShare(n, total int64) string {
	share := 100 * float64(n) / float64(total)
	if share < 10 {
		return fmt.Sprintf("%.1f%%", share)
	}
	return fmt.Sprintf("%.0f%%", share)
}

func failedCell(t api.TrafficCounts) ui.Cell {
	if t.Status5xx == 0 {
		return ui.C("0")
	}
	return ui.Cell{Text: fmt.Sprintf("%d (%s)", t.Status5xx, formatShare(t.Status5xx, t.Requests)), Style: ui.Red}
}
