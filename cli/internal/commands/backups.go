package commands

import (
	"bufio"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/backupfile"
	"github.com/shipwick/shipwick/pkg/spec"
)

// envBackupPassphrase is the variable `backups decrypt` reads the passphrase
// from: the name it has on the agent.
const envBackupPassphrase = "SHIPWICK_BACKUP_PASSPHRASE"

// backupListLimit is how many backups `shipwick backups` and `status` ask for.
const backupListLimit = 50

// backupCommands are the commands for scheduled backups and the agent's own state.
func (c *cli) backupCommands() []*cobra.Command {
	return []*cobra.Command{c.backupsCommand()}
}

func (c *cli) backupsCommand() *cobra.Command {
	var file string
	cmd := &cobra.Command{
		Use:   "backups [app]",
		Short: "List the backups the server keeps of an application",
		Long: `List the backups the server took of an application's volumes: on the schedule
under "backups" in deploy.yaml, or by hand with "shipwick backups run". They
are kept on the server and, when the agent has a bucket, in the bucket; the
agent removes the oldest beyond backups.keep.

  shipwick backups run my-db          take one now
  shipwick backups verify my-db       prove that the latest one restores
  shipwick backups restore my-db 12   put backup 12 back (the application must be stopped)

"shipwick backup" is the other kind: it downloads the volumes as they are right
now to this machine, and the server keeps nothing.

Without an argument, the application described by deploy.yaml is shown.`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := c.resolveApp(args, file)
			if err != nil {
				return err
			}
			return c.listBackups(cmd.Context(), name)
		},
	}
	fileFlag(cmd, &file)
	cmd.AddCommand(c.backupsRunCommand(), c.backupsVerifyCommand(), c.backupsRestoreCommand(),
		c.backupsDownloadCommand(), c.backupsRemoveCommand(), c.backupsDecryptCommand(), c.backupsAdoptCommand())
	return cmd
}

func (c *cli) listBackups(ctx context.Context, name string) error {
	cl, err := c.connect()
	if err != nil {
		return err
	}
	runs, err := cl.Backups(ctx, name, backupListLimit)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		c.ui.Println(fmt.Sprintf("The server keeps no backups of %s. Take one with: shipwick backups run %s", name, name))
		c.ui.Println("To have them taken on a schedule, add `backups` to deploy.yaml.")
		return nil
	}
	now := c.now()
	rows := make([][]ui.Cell, 0, len(runs))
	var lastFailure *api.BackupRun
	for i, r := range runs {
		size, where, verified := "-", "-", ui.C("-")
		if r.Status == api.BackupSucceeded {
			size, where = spec.FormatMemory(backupSize(r)), describeDestinations(r)
		}
		switch {
		case r.VerifiedAt != nil:
			verified = ui.Cell{Text: ui.RelativeTime(*r.VerifiedAt, now), Style: ui.Green}
		case r.VerifyError != "":
			verified = ui.Cell{Text: "failed", Style: ui.Red}
		}
		if r.Status == api.BackupFailed && lastFailure == nil {
			lastFailure = &runs[i]
		}
		rows = append(rows, []ui.Cell{
			ui.C(strconv.FormatInt(r.ID, 10)),
			ui.C(ui.RelativeTime(r.StartedAt, now)),
			ui.C(r.Trigger),
			ui.C(size),
			ui.C(where),
			backupStatusCell(r),
			verified,
		})
	}
	c.ui.Table([]string{"ID", "WHEN", "TRIGGER", "SIZE", "WHERE", "STATUS", "VERIFIED"}, rows)
	if lastFailure != nil {
		c.ui.Println()
		c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("Backup #%d failed: %s", lastFailure.ID, lastFailure.Error)))
	}
	return nil
}

func backupSize(r api.BackupRun) int64 {
	var n int64
	for _, v := range r.Volumes {
		n += v.SizeBytes
	}
	return n
}

func describeDestinations(r api.BackupRun) string {
	where := strings.Join(r.Destinations, ", ")
	if r.Encrypted {
		where += " (encrypted)"
	}
	return where
}

func backupStatusCell(r api.BackupRun) ui.Cell {
	switch {
	case r.Activity == api.BackupActivityVerify:
		return ui.Cell{Text: "verifying", Style: ui.Yellow}
	case r.Activity == api.BackupActivityRestore:
		return ui.Cell{Text: "restoring", Style: ui.Yellow}
	case r.Status == api.BackupSucceeded:
		return ui.Cell{Text: string(r.Status), Style: ui.Green}
	case r.Status == api.BackupFailed:
		return ui.Cell{Text: string(r.Status), Style: ui.Red}
	}
	return ui.Cell{Text: string(r.Status), Style: ui.Yellow}
}

func (c *cli) backupsRunCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "run <app>",
		Short: "Take a backup now and wait for it",
		Long: `Take a backup of the application's volumes now, the way the schedule would:
backups.before runs first, the application is stopped for the archive when
backups.stop says so, and the result is kept on the server and in the bucket.
An application without "backups" in its deploy.yaml is archived as it runs.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := c.resolveApp(args, "")
			if err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			run, err := cl.StartBackup(ctx, name)
			if err != nil {
				return err
			}
			c.ui.Progress("Backing up %s", name)
			final, err := c.awaitBackup(ctx, func() (api.BackupRun, error) {
				detail, err := cl.BackupRun(ctx, name, run.ID)
				return detail.BackupRun, err
			}, backupCompleted)
			c.ui.Done()
			if err != nil {
				return c.stoppedWaiting(ctx, err, "The backup continues on the server; see it with: shipwick backups "+name)
			}
			if final.Status != api.BackupSucceeded {
				c.ui.Failure("Backup #%d of %s failed: %s", final.ID, name, final.Error)
				return ErrReported
			}
			c.ui.Success("Backup #%d of %s: %s to %s", final.ID, name, spec.FormatMemory(backupSize(final)), describeDestinations(final))
			c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("  prove that it restores with: shipwick backups verify %s", name)))
			return nil
		},
	}
}

func (c *cli) backupsVerifyCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "verify <app> [id]",
		Short: "Prove that a backup restores into a working application",
		Long: `Restore a backup — the latest successful one, unless an id is given — into
scratch volumes, start one container of the application's current image on
them, and hold it to the application's health check. The container gets no
route and no name other applications could find it under; it and the volumes
are removed afterwards, whatever happened. The application itself keeps
running and is not touched.

The container runs with the application's environment: an application that
writes somewhere other than its volumes when it starts — another database, a
queue — does so here too.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := c.resolveApp(args[:1], "")
			if err != nil {
				return err
			}
			var id int64
			if len(args) == 2 {
				if id, err = parseBackupID(args[1]); err != nil {
					return err
				}
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			run, err := cl.VerifyBackup(ctx, name, id)
			if err != nil {
				return err
			}
			c.ui.Progress("Verifying backup #%d of %s", run.ID, name)
			var output string
			final, err := c.awaitBackup(ctx, func() (api.BackupRun, error) {
				detail, err := cl.BackupRun(ctx, name, run.ID)
				output = detail.VerifyOutput
				return detail.BackupRun, err
			}, backupIdle)
			c.ui.Done()
			if err != nil {
				return c.stoppedWaiting(ctx, err, "The verification continues on the server; see its verdict with: shipwick backups "+name)
			}
			if final.VerifiedAt == nil {
				if output != "" {
					c.ui.Println(c.ui.Styled(ui.Dim, "Last output of the container:"))
					c.ui.Println(output)
				}
				c.ui.Failure("Backup #%d of %s did not verify: %s", final.ID, name, final.VerifyError)
				return ErrReported
			}
			c.ui.Success("Backup #%d of %s restores: a container of the current version came up on its data", final.ID, name)
			return nil
		},
	}
}

func (c *cli) backupsRestoreCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore <app> <id>",
		Short: "Replace the application's volumes with a backup",
		Long: `Replace everything in the application's volumes with what a backup holds. The
application must be stopped, and stays stopped afterwards: look at it, then
"shipwick start".

To restore an archive from this machine instead, use "shipwick restore".`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := c.resolveApp(args[:1], "")
			if err != nil {
				return err
			}
			id, err := parseBackupID(args[1])
			if err != nil {
				return err
			}
			if !yes {
				if !isTerminal(c.in) {
					return errors.New("refusing to restore without confirmation; pass --yes")
				}
				c.ui.Printf("This replaces the data in the volumes of %s with backup #%d. What they hold now is lost.\nType the application name to confirm: ", name, id)
				answer, _ := bufio.NewReader(c.in).ReadString('\n')
				if strings.TrimSpace(answer) != name {
					return errors.New("cancelled: the name did not match")
				}
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			run, err := cl.RestoreBackup(ctx, name, id)
			if err != nil {
				return err
			}
			c.ui.Progress("Restoring backup #%d into %s", run.ID, name)
			final, err := c.awaitBackup(ctx, func() (api.BackupRun, error) {
				detail, err := cl.BackupRun(ctx, name, run.ID)
				return detail.BackupRun, err
			}, backupIdle)
			c.ui.Done()
			if err != nil {
				return c.stoppedWaiting(ctx, err, "The restore continues on the server; see how it ended with: shipwick status "+name)
			}
			if final.RestoredAt == nil {
				c.ui.Failure("Backup #%d was not restored into %s: %s", final.ID, name, final.RestoreError)
				return ErrReported
			}
			c.ui.Success("Restored backup #%d into %s", final.ID, name)
			c.ui.Println("Start it with: shipwick start " + name)
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

func (c *cli) backupsDownloadCommand() *cobra.Command {
	var dir string
	cmd := &cobra.Command{
		Use:   "download <app> <id>",
		Short: "Download a backup's archives to this machine",
		Long: `Download a backup, one tar archive per volume, named
<app>-<volume>-backup-<id>.tar. The archives arrive decrypted, as
"shipwick restore" takes them. Existing files are never overwritten.`,
		Args: cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := c.resolveApp(args[:1], "")
			if err != nil {
				return err
			}
			id, err := parseBackupID(args[1])
			if err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			run, err := cl.BackupRun(ctx, name, id)
			if err != nil {
				return err
			}
			if run.Status != api.BackupSucceeded {
				return fmt.Errorf("backup #%d of %s is %s; there is nothing to download", id, name, run.Status)
			}
			if dir != "" {
				if err := os.MkdirAll(dir, 0o755); err != nil {
					return err
				}
			}
			for _, v := range run.Volumes {
				path := filepath.Join(dir, fmt.Sprintf("%s-%s-backup-%d.tar", name, v.Volume, id))
				out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					return err
				}
				c.ui.Progress("Downloading volume %s of backup #%d", v.Volume, id)
				n, err := cl.BackupArchive(ctx, name, id, v.Volume, out)
				if err != nil {
					out.Close()
					os.Remove(path) // half an archive is worse than none: it looks like one
					c.ui.Done()
					return err
				}
				if err := out.Close(); err != nil {
					os.Remove(path)
					return err
				}
				c.ui.Success("%s (%s)", path, spec.FormatMemory(n))
			}
			return nil
		},
	}
	cmd.Flags().StringVarP(&dir, "output", "o", ".", "directory to write the archives to")
	return cmd
}

func (c *cli) backupsRemoveCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "rm <app> <id>",
		Short: "Remove a backup from the server and the bucket",
		Args:  cobra.ExactArgs(2),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := c.resolveApp(args[:1], "")
			if err != nil {
				return err
			}
			id, err := parseBackupID(args[1])
			if err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			if err := cl.DeleteBackup(cmd.Context(), name, id); err != nil {
				return err
			}
			c.ui.Success("Removed backup #%d of %s", id, name)
			return nil
		},
	}
}

func (c *cli) backupsDecryptCommand() *cobra.Command {
	var output string
	cmd := &cobra.Command{
		Use:   "decrypt <file>",
		Short: "Decrypt a backup file taken from the server or the bucket",
		Long: `Decrypt a file the agent wrote with SHIPWICK_BACKUP_PASSPHRASE set: an
application's <volume>.tar.enc, or shipwick.db.enc and encryption.key.enc of
the agent's own state. It runs on this machine and talks to no server.

The passphrase is read from ` + envBackupPassphrase + `, or asked for without echo.
The result is written next to the file, without the .enc ending, unless -o
says otherwise; an existing file is never overwritten.`,
		Args: cobra.ExactArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			return c.decryptBackup(args[0], output)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "file to write (default: the input without .enc)")
	return cmd
}

func (c *cli) decryptBackup(path, output string) error {
	in, err := os.Open(path)
	if err != nil {
		return err
	}
	defer in.Close()
	if output == "" {
		trimmed, ok := strings.CutSuffix(path, ".enc")
		if !ok {
			return fmt.Errorf("%s does not end in .enc; say where to write the result with -o", path)
		}
		output = trimmed
	}

	head := bufio.NewReader(in)
	if magic, _ := head.Peek(8); !backupfile.IsEncrypted(magic) {
		return fmt.Errorf("%s is not an encrypted Shipwick backup; if it is a tar archive already, restore it with: shipwick restore", path)
	}
	passphrase := c.getenv(envBackupPassphrase)
	if passphrase == "" {
		f, ok := c.in.(*os.File)
		if !ok || !isTerminal(c.in) {
			return fmt.Errorf("no passphrase: set %s, the value it has on the server", envBackupPassphrase)
		}
		c.ui.Printf("Passphrase: ")
		raw, err := term.ReadPassword(int(f.Fd()))
		c.ui.Println()
		if err != nil {
			return fmt.Errorf("read passphrase: %w", err)
		}
		passphrase = string(raw)
	}
	plain, err := backupfile.NewReader(head, passphrase)
	if err != nil {
		return err
	}

	out, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	n, err := io.Copy(out, plain)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	if err != nil {
		os.Remove(output) // part of a file that failed its check is not a file to keep
		return fmt.Errorf("%s: %w", path, err)
	}
	c.ui.Success("%s (%s)", output, spec.FormatMemory(n))
	return nil
}

func (c *cli) serverBackupCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "backup",
		Short: "Back up the agent's own state now: its database and encryption key",
		Long: `Back up the agent's database and the key that encrypts the secrets in it,
now. The agent does this once a day by itself; the backups go where
application backups go, under _agent/, and are only ever written encrypted:
SHIPWICK_BACKUP_PASSPHRASE must be set on the agent.

Restoring them is described in the handbook, under "Restoring the agent's
state".`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			run, err := cl.StartStateBackup(ctx)
			if err != nil {
				return err
			}
			c.ui.Progress("Backing up the agent's state")
			final, err := c.awaitBackup(ctx, func() (api.BackupRun, error) { return cl.StateBackup(ctx, run.ID) }, backupCompleted)
			c.ui.Done()
			if err != nil {
				return c.stoppedWaiting(ctx, err, "The backup continues on the server; see how it ended with: shipwick doctor")
			}
			if final.Status != api.BackupSucceeded {
				c.ui.Failure("The backup of the agent's state failed: %s", final.Error)
				return ErrReported
			}
			c.ui.Success("Agent state backed up: %s to %s (backup #%d)", spec.FormatMemory(backupSize(final)), describeDestinations(final), final.ID)
			return nil
		},
	}
}

func parseBackupID(s string) (int64, error) {
	id, err := strconv.ParseInt(strings.TrimPrefix(s, "#"), 10, 64)
	if err != nil || id < 1 {
		return 0, fmt.Errorf("%q is not a backup id; the ids are in the first column of: shipwick backups <app>", s)
	}
	return id, nil
}

func backupCompleted(r api.BackupRun) bool { return r.CompletedAt != nil }

// backupIdle is true once a verification or a restore has ended.
func backupIdle(r api.BackupRun) bool { return r.Activity == "" }

// awaitBackup polls a backup until done says so, riding out connectivity
// blips the way awaitDeployment does.
func (c *cli) awaitBackup(ctx context.Context, fetch func() (api.BackupRun, error), done func(api.BackupRun) bool) (api.BackupRun, error) {
	failures := 0
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		run, err := fetch()
		switch {
		case err == nil && done(run):
			return run, nil
		case err == nil:
			failures = 0
		case ctx.Err() != nil:
			return api.BackupRun{}, ctx.Err()
		default:
			var unreachable *client.UnreachableError
			if failures++; !errors.As(err, &unreachable) || failures >= maxPollFailures {
				return api.BackupRun{}, err
			}
			c.ui.Progress("Waiting for the agent to respond")
		}
		select {
		case <-ticker.C:
		case <-ctx.Done():
			return api.BackupRun{}, ctx.Err()
		}
	}
}

// stoppedWaiting turns an interrupted wait into a word about where the work
// goes on; any other error passes through.
func (c *cli) stoppedWaiting(ctx context.Context, err error, hint string) error {
	if ctx.Err() == nil {
		return err
	}
	c.ui.Println()
	c.ui.Println("Stopped waiting. " + hint)
	return ErrReported
}

// backupStatusFields is the Backups line of `shipwick status`, for an
// application with volumes: the schedule, the last backup and how many are
// kept — or the failure, if the last one failed. An agent too old to know
// backups simply has no such line.
func (c *cli) backupStatusFields(ctx context.Context, cl *client.Client, app api.ApplicationDetail) [][2]string {
	if app.Spec == nil || len(app.Spec.Volumes) == 0 {
		return nil
	}
	runs, err := cl.Backups(ctx, app.Name, backupListLimit)
	if err != nil {
		return nil
	}
	line := c.describeBackups(app.Name, app.Spec.Backups, runs, c.now())
	if line == "" {
		return nil
	}
	return [][2]string{{"Backups", line}}
}

func (c *cli) describeBackups(name string, b *spec.Backups, runs []api.BackupRun, now time.Time) string {
	var latest, good *api.BackupRun
	kept := 0
	for i, r := range runs {
		if r.Status == api.BackupRunning {
			continue
		}
		if latest == nil {
			latest = &runs[i]
		}
		if r.Status == api.BackupSucceeded {
			if kept++; good == nil {
				good = &runs[i]
			}
		}
	}
	when := "none scheduled"
	if b != nil {
		when = describeSchedule(b.Schedule)
	} else if latest == nil {
		return c.ui.Styled(ui.Yellow, "none") + c.ui.Styled(ui.Dim, "  (add `backups` to deploy.yaml, or take one with: shipwick backups run "+name+")")
	}
	switch {
	case latest == nil:
		return when + ", none taken yet"
	case latest.Status == api.BackupFailed:
		line := when + ", " + c.ui.Styled(ui.Red, "last one failed "+ui.RelativeTime(latest.StartedAt, now)) + ": " + truncate(latest.Error, 80)
		if good != nil {
			line += fmt.Sprintf(" (last good one %s)", ui.RelativeTime(good.StartedAt, now))
		}
		return line
	}
	return fmt.Sprintf("%s, last %s (%s), %d kept", when, ui.RelativeTime(good.StartedAt, now), spec.FormatMemory(backupSize(*good)), kept)
}

// describeSchedule says the common schedules in words — "daily at 03:00 UTC"
// — and repeats the expression for the rest.
func describeSchedule(expr string) string {
	fields := strings.Fields(expr)
	if len(fields) == 5 && fields[2] == "*" && fields[3] == "*" && fields[4] == "*" {
		minute, merr := strconv.Atoi(fields[0])
		hour, herr := strconv.Atoi(fields[1])
		if merr == nil && herr == nil {
			return fmt.Sprintf("daily at %02d:%02d UTC", hour, minute)
		}
		if merr == nil && fields[1] == "*" {
			return fmt.Sprintf("hourly at :%02d", minute)
		}
	}
	return "on " + expr + " (UTC)"
}

// checkStateBackup is doctor's line about the agent's own state: backed up,
// or why not and what to set. An agent too old to report it gets no line.
func (r *report) checkStateBackup(b *api.BackupStatus, now time.Time) {
	switch {
	case b == nil:
	case !b.Encrypted:
		r.hint("The encryption key exists only on this server. Set SHIPWICK_BACKUP_PASSPHRASE (and an S3 bucket) in /opt/shipwick/.env to back it up; losing it loses every secret")
	case b.StateError != "" && b.StateLastAt != nil:
		r.hint("The last backup of the agent's state failed: %s. The last good one is from %s; try again with: shipwick server backup", b.StateError, ui.RelativeTime(*b.StateLastAt, now))
	case b.StateError != "":
		r.hint("The agent's state has never been backed up: %s. Try again with: shipwick server backup", b.StateError)
	case b.StateLastAt == nil:
		r.hint("The agent's state has not been backed up yet; the first backup is taken within a minute of the agent starting. Take one now with: shipwick server backup")
	case b.Destination != api.BackupDestinationS3:
		r.ok("Agent state backed up %s to the server's own disk (set SHIPWICK_BACKUP_S3_* in /opt/shipwick/.env to keep a copy elsewhere)", ui.RelativeTime(*b.StateLastAt, now))
	default:
		r.ok("Agent state backed up %s to %s", ui.RelativeTime(*b.StateLastAt, now), b.Destination)
	}
}

// describeBackupPlan is the Backups line of `shipwick validate`: what the
// `backups` block will have the server do.
func describeBackupPlan(b spec.Backups) string {
	plan := fmt.Sprintf("%s, %d kept", describeSchedule(b.Schedule), b.Keep)
	if len(b.Before) > 0 {
		plan += ", after " + strings.Join(b.Before, " ")
		// The limit is worth a word when somebody chose it.
		if limit := b.BeforeLimit(); limit != spec.DefaultBackupBeforeTimeout {
			plan += " (" + shortDuration(limit) + " at most)"
		}
	}
	if b.Stop {
		plan += ", with the application stopped"
	}
	return plan
}

// shortDuration writes a duration the way deploy.yaml takes it: "2h", "90m",
// not "2h0m0s".
func shortDuration(d time.Duration) string {
	s := d.String()
	if strings.HasSuffix(s, "m0s") {
		s = strings.TrimSuffix(s, "0s")
	}
	if strings.HasSuffix(s, "h0m") {
		s = strings.TrimSuffix(s, "0m")
	}
	return s
}
