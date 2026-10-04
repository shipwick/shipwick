package commands

import (
	"archive/tar"
	"bytes"
	"compress/gzip"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/ui"
)

// A server with no way out is installed from a bundle: everything the
// installer would have fetched, made here, where there is a connection, and
// copied there by whatever means the network allows. The release's files are
// verified against its checksums as the installer would verify them, and
// among them the digests of its images, which the archive of images is
// checked against before it goes in (bundle_images.go). The bundle carries
// the checksum of its image archive, so the server can tell a damaged copy.

// bundleFiles are the release's files a bundle carries besides the CLI:
// names that scripts/build-release.sh and scripts/install.sh agree on.
const (
	bundleCompose   = "compose.production.yml"
	bundleInstaller = "install.sh"
	bundleChecksums = "checksums.txt"
	bundleImages    = "images.tar"
)

// bundleImage finds the three images in the release's compose file, where
// they are pinned to the release's version.
var bundleImage = regexp.MustCompile(`ghcr\.io/shipwick/(?:agent|dashboard|caddy):[A-Za-z0-9][A-Za-z0-9._-]*`)

type bundleOptions struct {
	version, arch, output string
	noPull                bool
}

func (c *cli) serverBundleCommand() *cobra.Command {
	var opts bundleOptions
	cmd := &cobra.Command{
		Use:   "bundle",
		Short: "Make the files that install or upgrade a server with no connection",
		Long: `Make a bundle that installs or upgrades a server with no connection to the
internet: the release's compose file, its installer and checksums, the
shipwick binary for the server, and the three images as one archive.

Run it on a machine that has a connection and Docker. The release's files are
verified against its published checksums; the images are pulled from the
registry for the server's architecture, saved, and the archive is checked
against the digests the release published for them (0.7.0 and later). With
--no-pull the images are the ones this machine has, and nothing proves them.

  shipwick server bundle --arch arm64

Copy the bundle to the server by whatever means there are, and there, as root:

  tar -xzf shipwick-v0.6.0-linux-arm64.tar.gz
  sh shipwick-v0.6.0-linux-arm64/install.sh

The installer downloads nothing and pulls nothing. Upgrading is the same with
the bundle of a newer release. Docker is not in the bundle: the server needs
Docker Engine and the Compose plugin before the installer runs.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			return c.serverBundle(cmd.Context(), opts)
		},
	}
	cmd.Flags().StringVar(&opts.version, "version", "", "release to bundle, such as v0.6.0 (default: the latest)")
	cmd.Flags().StringVar(&opts.arch, "arch", "amd64", "the server's architecture: amd64 or arm64")
	cmd.Flags().StringVarP(&opts.output, "output", "o", "", "file to write (default: shipwick-<version>-linux-<arch>.tar.gz here)")
	cmd.Flags().BoolVar(&opts.noPull, "no-pull", false, "save the images this machine has under the release's names instead of pulling them")
	return cmd
}

func (c *cli) serverBundle(ctx context.Context, opts bundleOptions) error {
	if opts.arch != "amd64" && opts.arch != "arm64" {
		return fmt.Errorf("--arch %q: the server runs on amd64 or arm64", opts.arch)
	}
	release := c.upgrade.withDefaults()
	tag := opts.version
	if tag == "" {
		latest, err := release.latestRelease(ctx)
		if err != nil {
			return err
		}
		tag = latest
	} else if _, ok := releaseTag(tag); !ok {
		return fmt.Errorf("--version %q is not a release version; releases look like v0.6.0", tag)
	}
	name := "shipwick-" + tag + "-linux-" + opts.arch
	output := opts.output
	if output == "" {
		output = name + ".tar.gz"
	}

	// Everything is gathered next to the output and verified there; the
	// output appears only when it is complete.
	work, err := os.MkdirTemp(filepath.Dir(output), ".shipwick-bundle-")
	if err != nil {
		return fmt.Errorf("create a working directory next to %s: %w", output, err)
	}
	defer os.RemoveAll(work)

	base := release.web + "/" + releaseRepo + "/releases/download/" + tag + "/"
	sums, err := release.fetch(ctx, base+bundleChecksums, "", 1<<20)
	if err != nil {
		return fmt.Errorf("download the checksums of release %s: %w", tag, err)
	}
	if err := os.WriteFile(filepath.Join(work, bundleChecksums), sums, 0o644); err != nil {
		return err
	}
	binary := "shipwick_linux_" + opts.arch
	files := []string{bundleChecksums}
	for _, asset := range []string{bundleCompose, bundleInstaller, binary} {
		c.ui.Progress("Downloading %s…", asset)
		err := release.verified(ctx, base, asset, sums, filepath.Join(work, asset))
		c.ui.Done()
		var missing *missingAssetError
		if errors.As(err, &missing) && asset == bundleInstaller {
			return fmt.Errorf("release %s does not publish its installer: a bundle can be made of 0.6.0 and later", tag)
		}
		if err != nil {
			return err
		}
		files = append(files, asset)
	}
	c.ui.Success("Release %s: %s, %s and %s match its checksums", tag, bundleCompose, bundleInstaller, binary)

	compose, err := os.ReadFile(filepath.Join(work, bundleCompose))
	if err != nil {
		return err
	}
	images := bundleImage.FindAllString(string(compose), -1)
	slices.Sort(images)
	images = slices.Compact(images)
	if len(images) != 3 {
		return fmt.Errorf("the compose file of release %s names %d Shipwick images, not three: %s", tag, len(images), strings.Join(images, ", "))
	}
	// What the release says its images are. Images this machine already had
	// were not served by the registry under the release's tags, and a release
	// before 0.7.0 says nothing: neither is held against digests.
	var digests []byte
	if !opts.noPull {
		err := release.verified(ctx, base, bundleDigests, sums, filepath.Join(work, bundleDigests))
		var missing *missingAssetError
		switch {
		case errors.As(err, &missing):
		case err != nil:
			return err
		default:
			if digests, err = os.ReadFile(filepath.Join(work, bundleDigests)); err != nil {
				return err
			}
		}
	}
	if err := c.saveImages(ctx, images, opts, filepath.Join(work, bundleImages)); err != nil {
		return err
	}
	switch {
	case digests != nil:
		c.ui.Progress("Checking the images against the release's digests…")
		err := verifyImageArchive(filepath.Join(work, bundleImages), images, digests, tag, "linux/"+opts.arch)
		c.ui.Done()
		if err != nil {
			return err
		}
		files = append(files, bundleDigests)
		c.ui.Success("The three images are the ones release %s published for linux/%s", tag, opts.arch)
	case opts.noPull:
		c.ui.Warn("The images are the ones this machine had under the release's names (--no-pull): they were not checked against the release's digests, and the installer will say so.")
	default:
		c.ui.Warn("Release %s does not publish the digests of its images (0.7.0 and later do): the images are what the registry serves under its tags, unchecked, and the installer will say so.", tag)
	}
	sum, err := sha256File(filepath.Join(work, bundleImages))
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(work, bundleImages+".sha256"), []byte(sum+"  "+bundleImages+"\n"), 0o644); err != nil {
		return err
	}
	files = append(files, bundleImages, bundleImages+".sha256")

	c.ui.Progress("Writing %s…", output)
	size, err := writeBundle(output, name, work, files)
	c.ui.Done()
	if err != nil {
		return err
	}
	c.ui.Success("Wrote %s (%d MB)", output, (size+1<<19)>>20)
	c.ui.Println()
	c.ui.Println("Copy it to the server, and there, as root:")
	c.ui.Println(c.ui.Styled(ui.Bold, "  tar -xzf "+filepath.Base(output)))
	c.ui.Println(c.ui.Styled(ui.Bold, "  sh "+name+"/install.sh"))
	c.ui.Println(c.ui.Styled(ui.Dim, "The server needs Docker Engine and the Compose plugin; nothing is downloaded there."))
	// Everything in the bundle follows from this one file; the installer
	// prints the same line, for whoever wants to compare the two.
	sumsDigest := sha256.Sum256(sums)
	c.ui.Println(c.ui.Styled(ui.Dim, "checksums.txt of the release: sha256 "+hex.EncodeToString(sumsDigest[:])))
	return nil
}

// saveImages pulls the images for the server's architecture and writes them
// to one archive, as `docker save` does: layers they share are stored once.
func (c *cli) saveImages(ctx context.Context, images []string, opts bundleOptions, dest string) error {
	build := c.build.withDefaults()
	platform := "linux/" + opts.arch
	if !opts.noPull {
		for _, image := range images {
			var out bytes.Buffer
			c.ui.Progress("Pulling %s (%s)…", image, platform)
			err := build.run(ctx, "", []string{"docker", "pull", "--quiet", "--platform", platform, image}, &out)
			c.ui.Done()
			if err != nil {
				return dockerFailed("pull "+image, err, out.String())
			}
		}
	}
	c.ui.Progress("Saving the images…")
	defer c.ui.Done()
	var out bytes.Buffer
	save := append([]string{"docker", "save", "--platform", platform, "--output", dest}, images...)
	err := build.run(ctx, "", save, &out)
	if err != nil && strings.Contains(out.String(), "unknown flag") {
		// A Docker from before `save --platform` keeps one platform of an
		// image, the one that was just pulled.
		out.Reset()
		err = build.run(ctx, "", append([]string{"docker", "save", "--output", dest}, images...), &out)
	}
	if err != nil {
		return dockerFailed("save the images", err, out.String())
	}
	return nil
}

// dockerFailed explains a docker command that did not work, with the last
// line it printed: that is where docker says why.
func dockerFailed(what string, err error, output string) error {
	if errors.Is(err, exec.ErrNotFound) {
		return fmt.Errorf("could not %s: docker is not installed on this machine, and the images of a bundle are pulled and saved with it", what)
	}
	lines := strings.Split(strings.TrimSpace(output), "\n")
	if last := strings.TrimSpace(lines[len(lines)-1]); last != "" {
		return fmt.Errorf("could not %s: %s", what, last)
	}
	return fmt.Errorf("could not %s: %w", what, err)
}

// missingAssetError means the release's checksums do not list the asset: the
// release does not have it.
type missingAssetError struct{ asset string }

func (e *missingAssetError) Error() string {
	return fmt.Sprintf("the release has no %s (looked in its checksums.txt)", e.asset)
}

// verified downloads one of the release's files to dest and refuses it unless
// it matches the release's checksum.
func (o upgradeOptions) verified(ctx context.Context, base, asset string, sums []byte, dest string) error {
	want, ok := checksumFor(sums, asset)
	if !ok {
		return &missingAssetError{asset: asset}
	}
	resp, err := o.get(ctx, base+asset, "")
	if err != nil {
		return fmt.Errorf("download %s: %w", base+asset, err)
	}
	defer resp.Body.Close()
	f, err := os.OpenFile(dest, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, sum), resp.Body); err != nil {
		return fmt.Errorf("download %s: %w", base+asset, err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		return fmt.Errorf("%s does not match the checksum published with the release; no bundle was written\n  downloaded from %s", asset, base+asset)
	}
	return f.Close()
}

func sha256File(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	sum := sha256.New()
	if _, err := io.Copy(sum, f); err != nil {
		return "", err
	}
	return hex.EncodeToString(sum.Sum(nil)), nil
}

// writeBundle packs files from dir into output as a gzipped tar with one
// directory, name, at its top. It is written under another name and renamed,
// so that a file with the bundle's name is always a whole bundle.
func writeBundle(output, name, dir string, files []string) (size int64, err error) {
	staged := output + ".partial"
	out, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o644)
	if err != nil {
		return 0, err
	}
	defer func() {
		out.Close()
		if err != nil {
			os.Remove(staged)
		}
	}()
	zw := gzip.NewWriter(out)
	tw := tar.NewWriter(zw)
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeDir, Name: name + "/", Mode: 0o755}); err != nil {
		return 0, err
	}
	for _, file := range files {
		if err := addToBundle(tw, name, dir, file); err != nil {
			return 0, fmt.Errorf("write %s: %w", output, err)
		}
	}
	if err := errors.Join(tw.Close(), zw.Close()); err != nil {
		return 0, fmt.Errorf("write %s: %w", output, err)
	}
	info, err := out.Stat()
	if err != nil {
		return 0, err
	}
	if err := out.Close(); err != nil {
		return 0, fmt.Errorf("write %s: %w", output, err)
	}
	return info.Size(), os.Rename(staged, output)
}

func addToBundle(tw *tar.Writer, name, dir, file string) error {
	f, err := os.Open(filepath.Join(dir, file))
	if err != nil {
		return err
	}
	defer f.Close()
	info, err := f.Stat()
	if err != nil {
		return err
	}
	// The installer and the binary are run where they land; the bits a
	// download was written with, on Windows above all, say nothing.
	mode := int64(0o644)
	if file == bundleInstaller || strings.HasPrefix(file, "shipwick_") {
		mode = 0o755
	}
	if err := tw.WriteHeader(&tar.Header{Typeflag: tar.TypeReg, Name: name + "/" + file, Mode: mode, Size: info.Size(), ModTime: info.ModTime()}); err != nil {
		return err
	}
	_, err = io.Copy(tw, f)
	return err
}
