package commands

import (
	"archive/tar"
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/spf13/cobra"
	"golang.org/x/term"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/backupfile"
	"github.com/shipwick/shipwick/pkg/spec"
)

// envExportPassphrase is the variable export and import read the passphrase
// of an export file from.
const envExportPassphrase = "SHIPWICK_EXPORT_PASSPHRASE"

// exportCommands are export, import and the standby server.
func (c *cli) exportCommands() []*cobra.Command {
	return []*cobra.Command{c.exportCommand(), c.importCommand(), c.standbyCommand()}
}

func (c *cli) exportCommand() *cobra.Command {
	var (
		output    string
		apps      []string
		toBackups bool
		list      bool
	)
	cmd := &cobra.Command{
		Use:   "export",
		Short: "Write everything the server runs into one encrypted file",
		Long: `Write every application's configuration and secrets, the stored secrets,
registry credentials and certificates, the images built by shipwick deploy,
the folders of static applications and an archive of every volume into one
file, for shipwick import on another server.

The file holds every secret of the server and is encrypted with a passphrase:
` + envExportPassphrase + `, or asked for twice without echo. Without the
passphrase the file cannot be read by anyone, including you.

Each application is held while its volumes are read, as for a backup; an
application with backups.before or backups.stop in its deploy.yaml gets the
same treatment here. A running database without either is copied as it is.

  shipwick export                  # shipwick-export-<time>.swexport
  shipwick export -o move.swexport --app db --app api
  shipwick export --to-backups     # to where the server keeps its backups,
                                   # encrypted with the server's passphrase
  shipwick export --list           # the exports kept there

The agent's own database and key are a different thing, for restoring this
same server: shipwick server backup.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			switch {
			case list:
				return c.listExports(cmd.Context())
			case toBackups:
				if output != "" || len(apps) > 0 {
					return errors.New("--to-backups writes the whole server to where its backups go; it takes neither -o nor --app")
				}
				return c.exportToBackups(cmd.Context())
			}
			return c.exportToFile(cmd.Context(), output, apps)
		},
	}
	cmd.Flags().StringVarP(&output, "output", "o", "", "file to write (default: shipwick-export-<time>.swexport)")
	cmd.Flags().StringArrayVar(&apps, "app", nil, "export only this application (repeatable)")
	cmd.Flags().BoolVar(&toBackups, "to-backups", false, "write the export on the server, to where its backups go")
	cmd.Flags().BoolVar(&list, "list", false, "list the exports the server keeps with its backups")
	return cmd
}

// exportPassphrase reads the passphrase of an export: from the environment,
// or from the terminal without echo — twice when a file is about to be
// written with it, since a typo there is a file nobody can open.
func (c *cli) exportPassphrase(confirm bool) (string, error) {
	passphrase := c.getenv(envExportPassphrase)
	if passphrase == "" {
		f, ok := c.in.(*os.File)
		if !ok || !isTerminal(c.in) {
			return "", fmt.Errorf("no passphrase: set %s", envExportPassphrase)
		}
		ask := func(label string) (string, error) {
			c.ui.Printf("%s: ", label)
			raw, err := term.ReadPassword(int(f.Fd()))
			c.ui.Println()
			if err != nil {
				return "", fmt.Errorf("read passphrase: %w", err)
			}
			return string(raw), nil
		}
		var err error
		if passphrase, err = ask("Passphrase"); err != nil {
			return "", err
		}
		if confirm {
			again, err := ask("Once more")
			if err != nil {
				return "", err
			}
			if again != passphrase {
				return "", errors.New("the two passphrases differ; nothing was exported")
			}
		}
	}
	if confirm && len(passphrase) < api.MinPassphraseLength {
		return "", fmt.Errorf("the passphrase must be at least %d characters long: it is all that protects every secret of the server", api.MinPassphraseLength)
	}
	return passphrase, nil
}

func (c *cli) exportToFile(ctx context.Context, output string, apps []string) error {
	for _, name := range apps {
		if err := spec.ValidateName(name); err != nil {
			return fmt.Errorf("--app: %w", err)
		}
	}
	cl, err := c.connect()
	if err != nil {
		return err
	}
	passphrase, err := c.exportPassphrase(true)
	if err != nil {
		return err
	}
	if output == "" {
		output = "shipwick-export-" + c.now().UTC().Format("20060102-150405") + ".swexport"
	}
	out, err := os.OpenFile(output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
	if err != nil {
		return err
	}
	c.ui.Progress("Exporting from %s", c.describeServer(cl.URL()))
	n, err := cl.Export(ctx, passphrase, apps, out)
	if cerr := out.Close(); err == nil {
		err = cerr
	}
	c.ui.Done()
	if err != nil {
		os.Remove(output) // part of an export is not a file to keep
		if client.IsCode(err, api.CodeDeploymentInProgress) {
			var apiErr *client.APIError
			errors.As(err, &apiErr)
			return fmt.Errorf("%s\n\nAn application that is being deployed or backed up cannot be exported at the same time. Run the export again when it is done", apiErr.Message)
		}
		return err
	}

	// Read back before it is called an export: a connection that ended
	// early leaves a file that looks like one from the outside.
	summary, err := readExportFile(output, passphrase)
	if err != nil {
		os.Remove(output)
		return fmt.Errorf("the file that arrived is not a whole export (%v); nothing was kept. Run the export again", err)
	}
	c.ui.Success("%s (%s)", output, spec.FormatMemory(n))
	c.ui.Println(summary.describe())
	c.ui.Println()
	c.ui.Println("On the new server: shipwick import " + output)
	c.ui.Println(c.ui.Styled(ui.Dim, "The file holds every secret of the server. Keep the passphrase: without it the file cannot be read."))
	return nil
}

// exportSummary is what a read through an export file finds.
type exportSummary struct {
	CreatedAt    time.Time `json:"created_at"`
	Shipwick     string    `json:"shipwick"`
	Applications []string  `json:"applications"`
	Secrets      []any     `json:"secrets"`
	Registries   []any     `json:"registries"`
	Certificates []any     `json:"certificates"`
}

func (s exportSummary) describe() string {
	parts := []string{plural(len(s.Applications), "application")}
	if len(s.Applications) > 0 {
		parts[0] += " (" + strings.Join(s.Applications, ", ") + ")"
	}
	for _, p := range []struct {
		n    int
		noun string
	}{{len(s.Secrets), "secret"}, {len(s.Registries), "registry credential"}, {len(s.Certificates), "certificate"}} {
		if p.n > 0 {
			parts = append(parts, plural(p.n, p.noun))
		}
	}
	return strings.Join(parts, ", ")
}

// readExportHead opens an export file and reads its first member, which
// says what it holds. A wrong passphrase shows here.
func readExportHead(r io.Reader, passphrase string) (exportSummary, *tar.Reader, error) {
	var summary exportSummary
	plain, err := backupfile.NewReader(bufio.NewReader(r), passphrase)
	if errors.Is(err, backupfile.ErrNotEncrypted) {
		return summary, nil, errors.New("it is not a file shipwick export wrote")
	} else if err != nil {
		return summary, nil, err
	}
	tr := tar.NewReader(plain)
	hdr, err := tr.Next()
	switch {
	case errors.Is(err, backupfile.ErrPassphrase):
		return summary, nil, errors.New("the passphrase does not match this export, or the file is damaged")
	case err != nil:
		return summary, nil, err
	case hdr.Name != "export.json":
		return summary, nil, errors.New("it is encrypted like an export but is not one: a backup file is decrypted with shipwick backups decrypt")
	}
	if err := json.NewDecoder(io.LimitReader(tr, 16<<20)).Decode(&summary); err != nil {
		return summary, nil, errors.New("its first entry does not read as JSON")
	}
	return summary, tr, nil
}

// readExportFile reads an export file to its end, which proves it whole:
// the encryption fails a file that was cut anywhere.
func readExportFile(path, passphrase string) (exportSummary, error) {
	f, err := os.Open(path)
	if err != nil {
		return exportSummary{}, err
	}
	defer f.Close()
	summary, tr, err := readExportHead(f, passphrase)
	if err != nil {
		return summary, err
	}
	for {
		if _, err := tr.Next(); errors.Is(err, io.EOF) {
			return summary, nil
		} else if err != nil {
			return summary, err
		}
	}
}

func (c *cli) exportToBackups(ctx context.Context) error {
	cl, err := c.connect()
	if err != nil {
		return err
	}
	run, err := cl.StartExport(ctx)
	if err != nil {
		return err
	}
	c.ui.Progress("Writing export #%d on the server", run.ID)
	final, err := c.awaitBackup(ctx, func() (api.BackupRun, error) { return cl.ExportRun(ctx, run.ID) }, backupCompleted)
	c.ui.Done()
	if err != nil {
		return c.stoppedWaiting(ctx, err, "The export continues on the server; see how it ended with: shipwick export --list")
	}
	if final.Status != api.BackupSucceeded {
		c.ui.Failure("Export #%d failed: %s", final.ID, final.Error)
		return ErrReported
	}
	var size int64
	for _, v := range final.Volumes {
		size += v.SizeBytes
	}
	c.ui.Success("Export #%d written to %s (%s)", final.ID, strings.Join(final.Destinations, " and "), spec.FormatMemory(size))
	return nil
}

func (c *cli) listExports(ctx context.Context) error {
	cl, err := c.connect()
	if err != nil {
		return err
	}
	runs, err := cl.Exports(ctx, backupListLimit)
	if err != nil {
		return err
	}
	if len(runs) == 0 {
		c.ui.Println("The server keeps no export. Write one with: shipwick export --to-backups, or on a schedule with SHIPWICK_EXPORT_SCHEDULE")
		return nil
	}
	rows := make([][]ui.Cell, 0, len(runs))
	for _, r := range runs {
		var size int64
		for _, v := range r.Volumes {
			size += v.SizeBytes
		}
		status := ui.Cell{Text: string(r.Status)}
		if r.Status == api.BackupFailed {
			status = ui.Cell{Text: "failed: " + r.Error, Style: ui.Red}
		}
		rows = append(rows, []ui.Cell{
			ui.C(fmt.Sprintf("#%d", r.ID)), ui.C(ui.RelativeTime(r.StartedAt, c.now())), ui.C(r.Trigger),
			ui.C(spec.FormatMemory(size)), ui.C(strings.Join(r.Destinations, ", ")), status,
		})
	}
	c.ui.Table([]string{"EXPORT", "WHEN", "TRIGGER", "SIZE", "WHERE", "STATUS"}, rows)
	return nil
}

func (c *cli) importCommand() *cobra.Command {
	var (
		overwrite bool
		stopped   bool
		yes       bool
		status    bool
	)
	cmd := &cobra.Command{
		Use:   "import <file>",
		Short: "Deploy what an export holds on this server, and restore its data",
		Long: `Take in a file written by shipwick export, on the server the current
context points at: store its secrets, registry credentials and certificates,
then deploy its applications one after the other and wait for each. An
application's volumes are restored before it first starts.

The order is the export's: applications without a domain first — what others
reach by name, such as a database — then the rest, each group oldest first.

What exists on this server under the same name is left as it is, and said
so, unless --overwrite is given; then applications are replaced together
with their volumes. --stopped deploys everything without starting it, which
is how a standby is kept: see shipwick standby.

Images from a registry are pulled here, with the credentials the export
brought. Images built by shipwick deploy travel in the file.

The passphrase is read from ` + envExportPassphrase + `, or asked for without echo.

  shipwick import move.swexport
  shipwick import move.swexport --stopped --overwrite --yes
  shipwick import --status         # the import that is running, or ran last`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			if status {
				return c.importStatus(cmd.Context())
			}
			if len(args) != 1 {
				return errors.New("name the file to import: shipwick import <file>")
			}
			return c.importFile(cmd.Context(), args[0], stopped, overwrite, yes)
		},
	}
	cmd.Flags().BoolVar(&overwrite, "overwrite", false, "replace what exists under the same name, volumes included")
	cmd.Flags().BoolVar(&stopped, "stopped", false, "deploy the applications without starting them")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask before overwriting")
	cmd.Flags().BoolVar(&status, "status", false, "show the import that is running, or ran last")
	return cmd
}

func (c *cli) importFile(ctx context.Context, path string, stopped, overwrite, yes bool) error {
	f, err := os.Open(path)
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	passphrase, err := c.exportPassphrase(false)
	if err != nil {
		return err
	}
	// Looked at here first: a wrong passphrase or a wrong file is found
	// before a byte is sent.
	summary, _, err := readExportHead(f, passphrase)
	if err != nil {
		return fmt.Errorf("%s: %w", path, err)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return err
	}
	cl, err := c.connect()
	if err != nil {
		return err
	}
	c.ui.Printf("Export of %s: %s\n", summary.CreatedAt.Local().Format("2006-01-02 15:04"), summary.describe())
	if overwrite && !yes {
		if !isTerminal(c.in) {
			return errors.New("refusing to overwrite without confirmation; pass --yes")
		}
		c.ui.Printf("This replaces the applications of the same names on %s, and the data in their volumes. What they hold now is lost.\nType overwrite to confirm: ", c.describeServer(cl.URL()))
		answer, _ := bufio.NewReader(c.in).ReadString('\n')
		if strings.TrimSpace(answer) != "overwrite" {
			return errors.New("cancelled")
		}
	}

	// The upload is read by the server as it deploys; what it has reached
	// is asked for on the side.
	watchCtx, stopWatching := context.WithCancel(ctx)
	var watching sync.WaitGroup
	watching.Add(1)
	go func() {
		defer watching.Done()
		c.watchImport(watchCtx, cl)
	}()
	c.ui.Progress("Importing into %s", c.describeServer(cl.URL()))
	result, err := cl.Import(ctx, f, info.Size(), passphrase, stopped, overwrite)
	stopWatching()
	watching.Wait()
	c.ui.Done()
	if err != nil {
		var unreachable *client.UnreachableError
		if errors.As(err, &unreachable) {
			return fmt.Errorf("%w\n\nThe import ends with the upload. See what it had finished with: shipwick import --status; run it again with --overwrite", err)
		}
		return err
	}
	return c.printImport(result)
}

// watchImport follows the import on the server and names the application
// it is at.
func (c *cli) watchImport(ctx context.Context, cl *client.Client) {
	ticker := time.NewTicker(c.pollInterval)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
		}
		im, err := cl.ImportStatus(ctx)
		if err != nil || im.Status != api.ImportRunning {
			continue
		}
		for i, a := range im.Applications {
			if a.Status == api.ImportAppRunning {
				c.ui.Progress("Importing %s (%d of %d)", a.Name, i+1, len(im.Applications))
			}
		}
	}
}

// printImport prints what an import did, and fails when it did not do all
// of it.
func (c *cli) printImport(im api.Import) error {
	for _, a := range im.Applications {
		switch a.Status {
		case api.ImportAppImported:
			line := a.Name + " " + a.Version
			if im.Stopped {
				line += ", stopped"
			}
			if len(a.Volumes) > 0 {
				line += fmt.Sprintf(" (%s restored: %s)", plural(len(a.Volumes), "volume"), strings.Join(a.Volumes, ", "))
			}
			c.ui.Success("%s", line)
		case api.ImportAppSkipped:
			c.ui.Warn("%s was not imported: %s", a.Name, a.Message)
		case api.ImportAppFailed:
			c.ui.Failure("%s: %s", a.Name, a.Message)
		default:
			c.ui.Println("  " + a.Name + ": " + a.Status)
		}
	}
	var stored []string
	for _, p := range []struct {
		n    int
		noun string
	}{{im.Secrets, "secret"}, {im.Registries, "registry credential"}, {im.Certificates, "certificate"}} {
		if p.n > 0 {
			stored = append(stored, plural(p.n, p.noun))
		}
	}
	if len(stored) > 0 {
		c.ui.Success("%s stored", strings.Join(stored, ", "))
	}
	for _, w := range im.Warnings {
		c.ui.Warn("%s", w)
	}
	switch {
	case im.Status == api.ImportRunning:
		c.ui.Println("The import is still running.")
		return nil
	case im.Error != "":
		c.ui.Failure("The import stopped: %s", im.Error)
		return ErrReported
	case im.Status == api.ImportFailed:
		c.ui.Println()
		c.ui.Println("Not everything was imported. What succeeded is in place; fix the rest and import again with --overwrite, or deploy it by hand.")
		return ErrReported
	}
	imported := 0
	for _, a := range im.Applications {
		if a.Status == api.ImportAppImported {
			imported++
		}
	}
	c.ui.Println()
	if imported == 0 {
		c.ui.Println("No application was imported.")
	} else if im.Stopped {
		c.ui.Println("Nothing was started. When this server is to take over: shipwick standby promote")
	} else {
		c.ui.Println("Point the DNS records of the applications' hostnames at this server: each is served once its record does. shipwick status <app> names the ones that wait.")
	}
	return nil
}

func (c *cli) importStatus(ctx context.Context) error {
	cl, err := c.connect()
	if err != nil {
		return err
	}
	im, err := cl.ImportStatus(ctx)
	if err != nil {
		return err
	}
	when := "started " + ui.RelativeTime(im.StartedAt, c.now())
	if im.CompletedAt != nil {
		when = "finished " + ui.RelativeTime(*im.CompletedAt, c.now())
	}
	c.ui.Printf("Import from %s, %s\n", im.Source, when)
	return c.printImport(im)
}

func (c *cli) standbyCommand() *cobra.Command {
	cmd := &cobra.Command{
		Use:   "standby",
		Short: "A second server kept ready: what it holds, and taking over",
		Long: `Shipwick does not fail over. What it does is keep a second server ready:
the first server writes an export on a schedule (SHIPWICK_EXPORT_SCHEDULE) to
its backup bucket, the second imports the newest one on a schedule
(SHIPWICK_STANDBY_SCHEDULE) with every application deployed and stopped, and
when the first server is gone, a person runs shipwick standby promote on the
second and changes the DNS records it prints.

That is minutes of downtime — the time to notice, to decide, and for DNS to
follow — and the data is as old as the last export. Without arguments, this
shows what the server holds for that day.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.standbyStatus(cmd.Context())
		},
	}
	cmd.AddCommand(c.standbyPromoteCommand(), c.standbyPullCommand())
	return cmd
}

func (c *cli) standbyStatus(ctx context.Context) error {
	cl, err := c.connect()
	if err != nil {
		return err
	}
	standby, err := cl.Standby(ctx)
	if err != nil {
		return err
	}
	if p := standby.Pull; p != nil {
		line := "Imports the newest export from the bucket on schedule " + p.Schedule + " (UTC)"
		switch {
		case p.LastError != "":
			c.ui.Println(line)
			c.ui.Failure("The last import failed %s: %s", ui.RelativeTime(deref(p.LastAt), c.now()), p.LastError)
		case p.LastAt != nil:
			c.ui.Println(fmt.Sprintf("%s; export #%d imported %s", line, p.LastExport, ui.RelativeTime(*p.LastAt, c.now())))
		default:
			c.ui.Println(line + "; none imported since the agent started")
		}
		c.ui.Println()
	}
	if len(standby.Applications) == 0 {
		c.ui.Println("No application is waiting for a promotion here.")
		if standby.Pull == nil {
			c.ui.Println("Import an export stopped with: shipwick import <file> --stopped, or set SHIPWICK_STANDBY_SCHEDULE on the agent.")
		}
		return nil
	}
	rows := make([][]ui.Cell, 0, len(standby.Applications))
	for _, a := range standby.Applications {
		rows = append(rows, []ui.Cell{ui.C(a.Name), ui.C(a.Version), ui.C(ui.RelativeTime(a.ImportedAt, c.now())), ui.C(strings.Join(a.Hostnames, ", "))})
	}
	c.ui.Table([]string{"APPLICATION", "VERSION", "IMPORTED", "HOSTNAMES"}, rows)
	c.ui.Println()
	c.ui.Println("Deployed and stopped. Start them, in this order, with: shipwick standby promote")
	return nil
}

func deref(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

func (c *cli) standbyPullCommand() *cobra.Command {
	return &cobra.Command{
		Use:   "pull",
		Short: "Import the newest export from the bucket now, stopped",
		Long: `Fetch the newest export from the bucket the agent is configured with and
import it with every application deployed and stopped, replacing the stopped
ones that are there: what the schedule does, now. Applications that run on
this server are never touched.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			im, err := cl.StandbyPull(ctx)
			if err != nil {
				return err
			}
			c.ui.Progress("Importing %s", im.Source)
			ticker := time.NewTicker(c.pollInterval)
			defer ticker.Stop()
			failures := 0
			for im.CompletedAt == nil {
				select {
				case <-ticker.C:
				case <-ctx.Done():
					c.ui.Done()
					return c.stoppedWaiting(ctx, ctx.Err(), "The import continues on the server; follow it with: shipwick import --status")
				}
				next, err := cl.ImportStatus(ctx)
				if err != nil {
					if failures++; failures >= maxPollFailures {
						c.ui.Done()
						return err
					}
					continue
				}
				failures, im = 0, next
				for i, a := range im.Applications {
					if a.Status == api.ImportAppRunning {
						c.ui.Progress("Importing %s (%d of %d)", a.Name, i+1, len(im.Applications))
					}
				}
			}
			c.ui.Done()
			return c.printImport(im)
		},
	}
}

func (c *cli) standbyPromoteCommand() *cobra.Command {
	var yes bool
	cmd := &cobra.Command{
		Use:   "promote",
		Short: "Start the applications of a standby and print the DNS records to change",
		Long: `Start every application that was imported stopped, in the order they were
imported, waiting for each to be ready, and print the DNS records that make
their hostnames reach this server.

Run it when the first server is gone, or about to be. Nothing here checks
that: two servers running the same applications against the same outside
services is yours to rule out. Visitors arrive once the records have changed
and their old values have expired; certificates are obtained when they do.

After a promotion this server is the service. Imports that leave applications
stopped no longer touch what runs here; remove SHIPWICK_STANDBY_SCHEDULE from
its configuration and give its backups a bucket prefix of their own.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			standby, err := cl.Standby(ctx)
			if err != nil {
				return err
			}
			if len(standby.Applications) == 0 {
				c.ui.Println("No application is waiting for a promotion on " + c.describeServer(cl.URL()) + ".")
				return nil
			}
			names := make([]string, 0, len(standby.Applications))
			for _, a := range standby.Applications {
				names = append(names, a.Name)
			}
			if !yes {
				if !isTerminal(c.in) {
					return errors.New("refusing to promote without confirmation; pass --yes")
				}
				c.ui.Printf("This starts %s on %s: %s.\nThe server they were exported from must no longer be serving.\nType promote to confirm: ",
					plural(len(names), "application"), c.describeServer(cl.URL()), strings.Join(names, ", "))
				answer, _ := bufio.NewReader(c.in).ReadString('\n')
				if strings.TrimSpace(answer) != "promote" {
					return errors.New("cancelled")
				}
			}
			c.ui.Progress("Starting %s", strings.Join(names, ", "))
			promotion, err := cl.Promote(ctx)
			c.ui.Done()
			if err != nil {
				return c.stoppedWaiting(ctx, err, "Applications that were started keep running; see them with: shipwick ps")
			}
			failed := false
			for _, a := range promotion.Applications {
				switch a.Status {
				case api.PromotedRunning:
					c.ui.Success("%s is running", a.Name)
				case api.PromotedStarted:
					c.ui.Warn("%s: %s", a.Name, a.Message)
				default:
					failed = true
					c.ui.Failure("%s could not be started: %s", a.Name, a.Message)
				}
			}
			c.printRecords(promotion.Records)
			if failed {
				return ErrReported
			}
			return nil
		},
	}
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// printRecords prints the DNS records a promotion asks for.
func (c *cli) printRecords(records []api.DNSRecord) {
	if len(records) == 0 {
		return
	}
	c.ui.Println()
	c.ui.Println("Change these DNS records. Until they have changed, visitors still go to the old server:")
	rows := make([][]ui.Cell, 0, len(records))
	unknown := false
	for _, r := range records {
		value := r.Value
		if value == "" {
			value, unknown = "<this server's address>", true
		}
		rows = append(rows, []ui.Cell{ui.C(r.Hostname), ui.C(r.Type), ui.C(value)})
	}
	c.ui.Table([]string{"HOSTNAME", "TYPE", "VALUE"}, rows)
	if unknown {
		c.ui.Println(c.ui.Styled(ui.Dim, "The agent does not know its own address: it learns it from SHIPWICK_AGENT_DOMAIN or SHIPWICK_DASHBOARD_DOMAIN."))
	}
}
