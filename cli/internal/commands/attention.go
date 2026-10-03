package commands

import (
	"sort"
	"strings"

	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
)

// attention is the last cell of an application's line in `shipwick ps`: what
// about it wants a look, in a few words, and nothing when nothing does. The
// details are one `shipwick status` away. An agent older than these fields
// reports neither, and the cell stays empty.
func attention(app api.Application) ui.Cell {
	var notes []string
	if p := app.CertificateProblem; p != nil {
		notes = append(notes, "certificate "+certificateWord(p.Status))
	}
	if app.AlertCount > 0 {
		notes = append(notes, plural(app.AlertCount, "alert"))
	}
	style := ui.Yellow
	if app.AlertSeverity == api.SeverityCritical {
		style = ui.Red
	}
	return ui.Cell{Text: strings.Join(notes, ", "), Style: style}
}

// certificateWord completes "certificate …" for a status that is not ok.
func certificateWord(status string) string {
	switch status {
	case api.CertWaitingForDNS:
		return "waiting for DNS"
	case api.CertExpiring:
		return "expiring"
	case api.CertObtaining:
		return "being obtained"
	}
	return status
}

// replicasFirst puts the containers that are on their way out after the
// application's replicas, each group in the order the agent listed it.
func replicasFirst(containers []api.Container) []api.Container {
	out := append([]api.Container(nil), containers...)
	sort.SliceStable(out, func(i, j int) bool { return !out[i].Stopping && out[j].Stopping })
	return out
}

// stoppingRow is the line of a container the agent is retiring: replaced, out
// of rotation, waiting for its process to exit. It has a replica's number in
// its name and nothing else of one — no health, no restarts that count.
func stoppingRow(ct api.Container) []ui.Cell {
	return []ui.Cell{
		ui.C(""),
		{Text: ct.Name, Style: ui.Dim},
		{Text: "stopping", Style: ui.Yellow},
		ui.C(""),
		ui.C(""),
		ui.C(""),
	}
}
