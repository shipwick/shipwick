package commands

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

const maxAuditEntries = 500

// auditFlags are the flags of `shipwick audit` as they were typed.
type auditFlags struct {
	app, actor, actorKind, since string
	actions, outcomes            []string
	limit                        int
	before                       int64
	format, output               string
}

// auditCommands returns `shipwick audit`.
func (c *cli) auditCommands() []*cobra.Command {
	var f auditFlags
	cmd := &cobra.Command{
		Use:   "audit",
		Short: "Show who changed what on the server, and when",
		Long: `Show who changed what on the server, and when.

Every request that changes something is recorded with the token that made it,
the address it came from, what it was about and how it was answered: deploys,
rollbacks, stops and starts, runs, deletions, secrets (by name), registry
logins, certificates, tokens, key rotation, backups, restores, exports and
imports. A request that was refused is recorded too. Reading is not.

Narrow it by application, by who, by what was done and by how it ended.
--action takes an action as the trail shows it (deploy, token.create) or the
start of a family with its dot (token., backup.), and can be repeated;
--outcome takes ok, refused and failed.

With --format or --output the whole of what matches is written out, not a
page: CSV for a spreadsheet, or JSON with one entry per line. Cells that a
spreadsheet would read as a formula are written as text. The export is itself
recorded in the trail.

The server keeps the trail for a year. Reading it needs admin.`,
		Example: `  shipwick audit
  shipwick audit --app my-api --since 7d
  shipwick audit --actor ci -n 200
  shipwick audit --action token. --action access. --outcome refused,failed
  shipwick audit --actor-kind person --since 30d
  shipwick audit --since 2026-01-01 --output audit-2026.csv
  shipwick audit --action deploy --format json | jq .actor.name`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			query, err := c.auditQuery(f)
			if err != nil {
				return err
			}
			if f.format != "" || f.output != "" {
				if cmd.Flags().Changed("lines") || cmd.Flags().Changed("before") {
					return errors.New("an export is everything that matches, not a page: leave -n and --before out, and narrow it with --since, --app, --actor, --action or --outcome")
				}
				return c.auditExport(cmd, query, f)
			}
			if f.limit < 1 || f.limit > maxAuditEntries {
				return fmt.Errorf("-n must be between 1 and %d", maxAuditEntries)
			}
			if f.before < 0 {
				return fmt.Errorf("--before takes the id of an entry, as the last line of a full page prints it")
			}
			query.Limit, query.Before = f.limit, f.before

			cl, err := c.connect()
			if err != nil {
				return err
			}
			page, err := cl.Audit(cmd.Context(), query)
			if err != nil {
				return olderThanAudit(err)
			}
			entries := page.Data
			if len(entries) == 0 {
				if f.narrowed() || f.before != 0 {
					c.ui.Println("Nothing recorded that matches.")
				} else {
					c.ui.Println("Nothing recorded yet: the trail starts with the first change made through this agent.")
				}
				return nil
			}

			rows := make([][]ui.Cell, 0, len(entries))
			for _, e := range entries {
				rows = append(rows, []ui.Cell{
					ui.C(e.At.Local().Format("2006-01-02 15:04:05")),
					ui.C(e.Actor.Name),
					ui.C(e.Action),
					ui.C(auditSubject(e)),
					auditResult(e),
					ui.C(auditOrigin(e)),
					ui.C(e.Detail),
				})
			}
			c.ui.Table([]string{"WHEN", "WHO", "ACTION", "ON", "RESULT", "FROM", "DETAIL"}, rows)
			// The agent says whether there is more; one from before 0.7 does
			// not, and a full page is the best guess there is.
			more := len(entries) == f.limit
			if page.More != nil {
				more = *page.More
			}
			if more {
				c.ui.Println()
				c.ui.Println(c.ui.Styled(ui.Dim, "Older entries: "+olderAuditCommand(f, entries[len(entries)-1].ID)))
			}
			return nil
		},
	}
	cmd.Flags().StringVar(&f.app, "app", "", "only what was done to this application")
	cmd.Flags().StringVar(&f.actor, "actor", "", "only what this token or this person did")
	cmd.Flags().StringVar(&f.actorKind, "actor-kind", "", "only what tokens did, or only what people who signed in did: token or person")
	cmd.Flags().StringArrayVar(&f.actions, "action", nil, "only this action, or with a dot at its end this family of actions (token.); repeat for several")
	cmd.Flags().StringArrayVar(&f.outcomes, "outcome", nil, "only what ended this way: ok, refused or failed; several separated by commas")
	cmd.Flags().StringVar(&f.since, "since", "", "how far back: days or hours (7d, 24h) or a date (2026-09-01)")
	cmd.Flags().IntVarP(&f.limit, "lines", "n", 50, "how many entries, newest first")
	cmd.Flags().Int64Var(&f.before, "before", 0, "continue with the entries older than the one with this id")
	cmd.Flags().StringVar(&f.format, "format", "", "write out everything that matches instead of a page: csv or json (one entry per line)")
	cmd.Flags().StringVarP(&f.output, "output", "o", "", "the file to write the export to (default: standard output)")
	return []*cobra.Command{cmd}
}

func (f auditFlags) narrowed() bool {
	return f.app != "" || f.actor != "" || f.actorKind != "" || f.since != "" || len(f.actions) > 0 || len(f.outcomes) > 0
}

// auditQuery checks the filters and returns them as the agent takes them.
func (c *cli) auditQuery(f auditFlags) (client.AuditQuery, error) {
	query := client.AuditQuery{Application: f.app, Actor: f.actor, Actions: api.SplitList(f.actions), Outcomes: api.SplitList(f.outcomes)}
	if f.app != "" {
		if err := spec.ValidateName(f.app); err != nil {
			return query, err
		}
	}
	switch f.actorKind {
	case "":
	case "token":
		query.ActorKind = api.ActorToken
	case "person", api.ActorUser:
		query.ActorKind = api.ActorUser
	default:
		return query, fmt.Errorf("--actor-kind: %q is neither token nor person", f.actorKind)
	}
	if len(query.Actions) > api.MaxAuditActions {
		return query, fmt.Errorf("--action: at most %d at once; the start of a family covers all of it, e.g. --action backup.", api.MaxAuditActions)
	}
	for _, action := range query.Actions {
		if err := api.ValidateAuditAction(action); err != nil {
			return query, fmt.Errorf("--action: %w", err)
		}
	}
	for _, outcome := range query.Outcomes {
		if err := api.ValidateAuditOutcome(outcome); err != nil {
			return query, fmt.Errorf("--outcome: %w", err)
		}
	}
	if f.since != "" {
		at, err := api.ParseSince(f.since, c.now())
		if err != nil {
			return query, fmt.Errorf("--since: %w", err)
		}
		query.Since = at
	}
	return query, nil
}

// olderThanAudit puts into words what an agent that predates the trail, its
// filters or its export answers.
func olderThanAudit(err error) error {
	switch {
	case errors.Is(err, client.ErrAuditFiltersUnknown):
		return errors.New("the agent is older than this shipwick: it filters the audit trail by application, actor and time only, and would have ignored --action, --outcome and --actor-kind\n\nCompare versions with: shipwick server status")
	case client.IsCode(err, api.CodeEndpointNotFound):
		return errors.New("the agent is older than this shipwick and keeps no audit trail\n\nCompare versions with: shipwick server status")
	}
	return err
}

// auditFormat is the format of an export: what --format says, or what the
// name of the file does.
func auditFormat(f auditFlags) (string, error) {
	switch f.format {
	case api.AuditFormatCSV, api.AuditFormatJSON:
		return f.format, nil
	case "":
		switch strings.ToLower(filepath.Ext(f.output)) {
		case ".csv":
			return api.AuditFormatCSV, nil
		case ".json", ".ndjson", ".jsonl":
			return api.AuditFormatJSON, nil
		}
		return "", fmt.Errorf("--output %s does not say which format: add --format csv or --format json", f.output)
	}
	return "", fmt.Errorf("--format: %q is neither csv nor json", f.format)
}

// auditExport writes the whole of what matches to the file, or to standard
// output. A file that did not arrive whole is not kept: it would look like
// the trail and lack its end.
func (c *cli) auditExport(cmd *cobra.Command, query client.AuditQuery, f auditFlags) error {
	format, err := auditFormat(f)
	if err != nil {
		return err
	}
	cl, err := c.connect()
	if err != nil {
		return err
	}
	if f.output == "" {
		_, err := cl.ExportAudit(cmd.Context(), query, format, cmd.OutOrStdout())
		return olderThanAuditExport(err)
	}
	// Names, addresses and who did what: for its owner to read.
	out, err := os.OpenFile(f.output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		if errors.Is(err, os.ErrExist) {
			return fmt.Errorf("%s exists already; choose another name, or remove it first", f.output)
		}
		return err
	}
	_, err = cl.ExportAudit(cmd.Context(), query, format, out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(f.output)
		return olderThanAuditExport(err)
	}
	c.ui.Success("Wrote the audit trail to %s", f.output)
	c.ui.Println(c.ui.Styled(ui.Dim, "The export is recorded in the trail, with who took it."))
	return nil
}

func olderThanAuditExport(err error) error {
	if client.IsCode(err, api.CodeEndpointNotFound) {
		return errors.New("the agent is older than this shipwick and cannot export its audit trail\n\nCompare versions with: shipwick server status")
	}
	return err
}

func olderAuditCommand(f auditFlags, last int64) string {
	cmd := "shipwick audit"
	if f.app != "" {
		cmd += " --app " + f.app
	}
	if f.actor != "" {
		cmd += " --actor " + f.actor
	}
	if f.actorKind != "" {
		cmd += " --actor-kind " + f.actorKind
	}
	for _, action := range api.SplitList(f.actions) {
		cmd += " --action " + action
	}
	if outcomes := api.SplitList(f.outcomes); len(outcomes) > 0 {
		cmd += " --outcome " + strings.Join(outcomes, ",")
	}
	if f.since != "" {
		cmd += " --since " + f.since
	}
	if f.limit != 50 {
		cmd += " -n " + strconv.Itoa(f.limit)
	}
	return cmd + " --before " + strconv.FormatInt(last, 10)
}
