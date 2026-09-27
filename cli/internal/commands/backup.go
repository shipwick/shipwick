package commands

import (
	"archive/tar"
	"bufio"
	"bytes"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/spec"
)

// volumeCommands returns the backup, restore commands.
func (c *cli) volumeCommands() []*cobra.Command {
	return []*cobra.Command{c.backupCommand(), c.restoreCommand()}
}

func (c *cli) backupCommand() *cobra.Command {
	var file, volume, dir string
	cmd := &cobra.Command{
		Use:   "backup [app]",
		Short: "Download the volumes of an application as tar archives",
		Long: `backup downloads the volumes of an application, one tar archive each, named
<app>-<volume>-<UTC timestamp>.tar. Existing files are never overwritten.

The copy is taken while the application runs, unless it is stopped. A
database that is being written to may not be consistent in the copy: stop the
application first, or use its own dump tool with "shipwick run".`,
		Args: cobra.MaximumNArgs(1),
		RunE: func(cmd *cobra.Command, args []string) error {
			name, err := c.resolveApp(args, file)
			if err != nil {
				return err
			}
			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			app, err := cl.Application(ctx, name)
			if err != nil {
				return err
			}
			all, err := cl.Volumes(ctx, name)
			if err != nil {
				return err
			}
			volumes, err := pickVolumes(name, all, volume)
			if err != nil {
				return err
			}
			if app.Replicas.Running > 0 {
				c.ui.Warn("%s is running; a copy taken now may be inconsistent. For a database, stop it first: shipwick stop %s", name, name)
			}

			stamp := c.now().UTC().Format("20060102-150405")
			for _, v := range volumes {
				path := filepath.Join(dir, fmt.Sprintf("%s-%s-%s.tar", name, v.Name, stamp))
				out, err := os.OpenFile(path, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
				if err != nil {
					return err
				}
				c.ui.Progress("Backing up volume %s of %s", v.Name, name)
				n, err := cl.Backup(ctx, name, v.Name, out)
				if err != nil {
					out.Close()
					os.Remove(path) // half a backup is worse than none: it looks like one
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
	fileFlag(cmd, &file)
	cmd.Flags().StringVar(&volume, "volume", "", "back up only this volume (default: every volume)")
	cmd.Flags().StringVarP(&dir, "output", "o", ".", "directory to write the archives to")
	return cmd
}

func (c *cli) restoreCommand() *cobra.Command {
	var file, volume string
	var yes bool
	cmd := &cobra.Command{
		Use:   "restore [app] <archive.tar>",
		Short: "Replace the data of a volume with a backup",
		Long: `restore replaces everything in a volume with the contents of a tar archive, as
written by "shipwick backup". The application must be stopped, and stays
stopped afterwards.`,
		Args: cobra.RangeArgs(1, 2),
		RunE: func(cmd *cobra.Command, args []string) error {
			archive := args[len(args)-1]
			name, err := c.resolveApp(args[:len(args)-1], file)
			if err != nil {
				return err
			}
			in, size, err := openArchive(archive)
			if err != nil {
				return err
			}
			defer in.Close()

			cl, err := c.connect()
			if err != nil {
				return err
			}
			ctx := cmd.Context()
			if volume == "" {
				all, err := cl.Volumes(ctx, name)
				if err != nil {
					return err
				}
				volumes, err := pickVolumes(name, all, "")
				if err != nil {
					return err
				}
				if len(volumes) > 1 {
					return fmt.Errorf("%s has %d volumes; name one with --volume: %s", name, len(volumes), volumeNames(volumes))
				}
				volume = volumes[0].Name
			}

			if !yes {
				if !isTerminal(c.in) {
					return errors.New("refusing to restore without confirmation; pass --yes")
				}
				c.ui.Printf("This replaces the data of volume %s of %s with %s. The application must be stopped and is not started afterwards.\nContinue? [y/N] ", volume, name, archive)
				answer, _ := bufio.NewReader(c.in).ReadString('\n')
				if a := strings.ToLower(strings.TrimSpace(answer)); a != "y" && a != "yes" {
					return errors.New("cancelled")
				}
			}

			var body io.Reader = in
			if c.ui.IsTerminal() {
				body = &uploadProgress{r: in, total: size, show: func(pct int) {
					c.ui.Progress("Uploading %s  %d%%", filepath.Base(archive), pct)
				}}
			}
			if err := cl.Restore(ctx, name, volume, body, size); err != nil {
				c.ui.Done()
				return err
			}
			c.ui.Success("Restored volume %s of %s from %s", volume, name, archive)
			c.ui.Println("Start it with: shipwick start " + name)
			return nil
		},
	}
	fileFlag(cmd, &file)
	cmd.Flags().StringVar(&volume, "volume", "", "the volume to restore (default: the application's only volume)")
	cmd.Flags().BoolVarP(&yes, "yes", "y", false, "do not ask for confirmation")
	return cmd
}

// pickVolumes narrows the application's volumes to the one asked for, and
// turns "none" into an error that says so.
func pickVolumes(name string, volumes []api.Volume, want string) ([]api.Volume, error) {
	if len(volumes) == 0 {
		return nil, fmt.Errorf("%s has no volumes; there is nothing to back up or restore", name)
	}
	if want == "" {
		return volumes, nil
	}
	for _, v := range volumes {
		if v.Name == want {
			return []api.Volume{v}, nil
		}
	}
	return nil, fmt.Errorf("%s has no volume %q; it has: %s", name, want, volumeNames(volumes))
}

func volumeNames(volumes []api.Volume) string {
	names := make([]string, 0, len(volumes))
	for _, v := range volumes {
		names = append(names, v.Name)
	}
	return strings.Join(names, ", ")
}

// openArchive opens a backup for upload and checks that it starts like a tar
// archive, so that a wrong file — a .tar.gz, a SQL dump — is refused here
// rather than after the server has emptied the volume. The agent checks
// again.
func openArchive(path string) (*os.File, int64, error) {
	f, err := os.Open(path)
	if errors.Is(err, fs.ErrNotExist) {
		return nil, 0, fmt.Errorf("%s not found", path)
	} else if err != nil {
		return nil, 0, err
	}
	info, err := f.Stat()
	if err != nil {
		f.Close()
		return nil, 0, err
	}
	head := make([]byte, 512)
	if _, err := io.ReadFull(f, head); err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("%s is not a tar archive", path)
	}
	if _, err := tar.NewReader(bytes.NewReader(head)).Next(); err != nil {
		f.Close()
		return nil, 0, fmt.Errorf("%s is not a tar archive; restore takes the .tar written by shipwick backup", path)
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		f.Close()
		return nil, 0, err
	}
	return f, info.Size(), nil
}

// uploadProgress reports the percentage of an upload as it is read.
type uploadProgress struct {
	r     io.Reader
	total int64
	sent  int64
	last  int
	show  func(pct int)
}

func (p *uploadProgress) Read(b []byte) (int, error) {
	n, err := p.r.Read(b)
	p.sent += int64(n)
	if p.total > 0 {
		if pct := int(p.sent * 100 / p.total); pct != p.last {
			p.last = pct
			p.show(pct)
		}
	}
	return n, err
}
