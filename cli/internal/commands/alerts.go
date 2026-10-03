package commands

import (
	"fmt"

	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// describeDisk renders the disk line of `server status`.
func describeDisk(d api.DiskUsage) string {
	percent := 0
	if d.TotalBytes > 0 {
		percent = int(100 * d.UsedBytes / d.TotalBytes)
	}
	return fmt.Sprintf("%s of %s used (%d%%)", spec.FormatMemory(d.UsedBytes), spec.FormatMemory(d.TotalBytes), percent)
}

// printAlerts lists the server's active alerts, one line each: the message
// the agent wrote already says what is wrong and where to look.
func (c *cli) printAlerts(alerts []api.Alert) {
	if len(alerts) == 0 {
		return
	}
	c.ui.Println()
	for _, a := range alerts {
		mark := c.ui.Styled(ui.Yellow, "!")
		if a.Severity == api.SeverityCritical {
			mark = c.ui.Styled(ui.Red, "✗")
		}
		c.ui.Println(mark + " " + a.Message)
	}
}

// alerts adds the server's active alerts to doctor's report: a critical one
// is a problem, a warning something worth a look. An agent from before
// alerts existed reports none.
func (r *report) alerts(alerts []api.Alert) {
	for _, a := range alerts {
		if a.Severity == api.SeverityCritical {
			r.problem("%s", a.Message)
		} else {
			r.hint("%s", a.Message)
		}
	}
}
