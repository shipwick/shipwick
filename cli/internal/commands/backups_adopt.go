package commands

import (
	"fmt"
	"strconv"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

func (c *cli) backupsAdoptCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "adopt [app]",
		Short: "Record the backups the server's directory and bucket hold and its database has forgotten",
		Long: `Record the backups that are in the server's backup directory or in its bucket
and that the agent's database does not know: after the agent's state was
restored from a backup of it, those are the backups taken since. Without an
argument everything is looked at — every application's backups, the agent's
own state and the exports; with one, that application's.

An adopted backup is listed with the trigger "adopted", with the time, the
volumes and the sizes its files have. From then on it is a backup like any
other: it can be verified, restored and downloaded, and counts towards
backups.keep. Nothing is changed in the directory or the bucket, and running
the command again adopts nothing twice.

A bucket that another installation writes to is refused, as it is for
backups.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name := ""
			if len(args) == 1 {
				if err := spec.ValidateName(args[0]); err != nil {
					return err
				}
				name = args[0]
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			c.ui.Progress("Looking at what the server's directory and bucket hold")
			adoption, err := cl.AdoptBackups(cmd.Context(), name)
			c.ui.Done()
			if err != nil {
				return err
			}
			c.printAdoption(name, adoption)
			return nil
		},
	}
}

func (c *cli) printAdoption(name string, adoption api.BackupAdoption) {
	of := ""
	if name != "" {
		of = " of " + name
	}
	if len(adoption.Adopted) == 0 {
		c.ui.Println(fmt.Sprintf("Nothing to adopt: the server knows every backup%s that its directory and bucket hold.", of))
	} else {
		now := c.now()
		rows := make([][]ui.Cell, 0, len(adoption.Adopted))
		for _, a := range adoption.Adopted {
			rows = append(rows, []ui.Cell{
				ui.C(adoptedOwner(a.Kind, a.Application)),
				ui.C(strconv.FormatInt(a.Backup.ID, 10)),
				ui.C(ui.RelativeTime(a.Backup.StartedAt, now)),
				ui.C(spec.FormatMemory(backupSize(a.Backup))),
				ui.C(describeDestinations(a.Backup)),
			})
		}
		c.ui.Table([]string{"BACKUP OF", "ID", "WHEN", "SIZE", "WHERE"}, rows)
		c.ui.Println()
		c.ui.Success("Adopted %s%s", plural(len(adoption.Adopted), "backup"), of)
		c.ui.Println(c.ui.Styled(ui.Dim, "  an application's are listed with: shipwick backups <app>; prove that one restores with: shipwick backups verify <app> <id>"))
	}
	for _, s := range adoption.Skipped {
		c.ui.Warn("Backup #%d of %s was left alone: %s", s.ID, adoptedOwner(s.Kind, s.Application), s.Reason)
	}
}

// adoptedOwner names what a backup is a backup of.
func adoptedOwner(kind, application string) string {
	switch kind {
	case api.BackupKindState:
		return "the agent's state"
	case api.BackupKindExport:
		return "an export"
	}
	return application
}
