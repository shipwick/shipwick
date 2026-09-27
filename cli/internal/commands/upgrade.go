package commands

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net/http"
	"os"
	"path"
	"path/filepath"
	"runtime"
	"strings"
	"time"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/cli/internal/ui"
	"github.com/shipwick/shipwick/pkg/version"
)

// releaseRepo is the GitHub repository whose releases carry the CLI binaries
// and checksums.txt (see scripts/build-release.sh for the asset names).
const releaseRepo = "shipwick/shipwick"

// installerCommand is how a server is upgraded. The CLI never does that
// itself: the installer runs on the server with Docker access, the CLI has
// neither.
const installerCommand = "curl -fsSL https://get.shipwick.com | sh"

// upgradeOptions are the dependencies of `shipwick upgrade`. Zero fields take
// their defaults; tests fill them in.
type upgradeOptions struct {
	// api and web are GitHub's API and website.
	api, web string
	http     *http.Client
	// executable locates the running binary, symlinks resolved.
	executable   func() (string, error)
	goos, goarch string
	version      string
}

func (o upgradeOptions) withDefaults() upgradeOptions {
	if o.api == "" {
		o.api = "https://api.github.com"
	}
	if o.web == "" {
		o.web = "https://github.com"
	}
	if o.http == nil {
		o.http = &http.Client{Timeout: 5 * time.Minute}
	}
	if o.executable == nil {
		o.executable = func() (string, error) {
			exe, err := os.Executable()
			if err != nil {
				return "", err
			}
			return filepath.EvalSymlinks(exe)
		}
	}
	if o.goos == "" {
		o.goos = runtime.GOOS
	}
	if o.goarch == "" {
		o.goarch = runtime.GOARCH
	}
	if o.version == "" {
		o.version = version.Version
	}
	return o
}

// upgradeCommands returns the upgrade, context commands.
func (c *cli) upgradeCommands() []*cobra.Command {
	return []*cobra.Command{c.upgradeCommand(), c.contextCommand()}
}

func (c *cli) upgradeCommand() *cobra.Command {
	var check bool
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Replace this shipwick with the latest release",
		Long: `Replace this shipwick with the latest release.

The release is downloaded from GitHub and verified against its published
checksums before the binary is swapped. A shipwick installed with Homebrew or
winget is left to the package manager; the command tells you what to run.

The server is not upgraded by this command: the installer does that, on the
server, with access to Docker. If the server is behind, the command says so.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := c.upgrade.withDefaults()
			ctx := cmd.Context()
			latest, err := opts.latestRelease(ctx)
			if err != nil {
				return err
			}
			if err := c.upgradeBinary(ctx, opts, latest, check); err != nil {
				return err
			}
			c.ui.Println()
			c.compareServer(ctx, latest)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "report what is available and change nothing")
	return cmd
}

// upgradeBinary brings the running binary up to latest, or explains how.
func (c *cli) upgradeBinary(ctx context.Context, opts upgradeOptions, latest string, check bool) error {
	if _, ok := version.Parse(opts.version); !ok {
		c.ui.Println(fmt.Sprintf("This shipwick is a development build (%s); the latest release is %s.", opts.version, latest))
		return nil
	}
	if version.Compare(opts.version, latest) >= 0 {
		c.ui.Println(fmt.Sprintf("shipwick %s is up to date.", opts.version))
		return nil
	}

	exe, err := opts.executable()
	if err != nil {
		return fmt.Errorf("locate the running shipwick: %w", err)
	}
	switch installMethod(exe) {
	case "brew":
		c.ui.Println(fmt.Sprintf("shipwick %s was installed with Homebrew; %s is available.\n\nUpgrade with: brew upgrade shipwick", opts.version, latest))
		return nil
	case "winget":
		c.ui.Println(fmt.Sprintf("shipwick %s was installed with winget; %s is available.\n\nUpgrade with: winget upgrade Shipwick.Shipwick", opts.version, latest))
		return nil
	}
	if check {
		c.ui.Println(fmt.Sprintf("shipwick %s is installed; %s is available.\n\nUpgrade with: shipwick upgrade", opts.version, latest))
		return nil
	}

	c.ui.Progress("Downloading shipwick %s…", latest)
	err = c.replaceBinary(ctx, opts, latest, exe)
	c.ui.Done()
	if err != nil {
		return err
	}
	c.ui.Success("Upgraded shipwick %s → %s", opts.version, latest)
	c.ui.Println(c.ui.Styled(ui.Dim, "  "+exe))
	return nil
}

// replaceBinary downloads the release asset for this platform next to the
// running binary, verifies it against checksums.txt and renames it into
// place. Nothing about the old binary changes until the new one is verified.
func (c *cli) replaceBinary(ctx context.Context, opts upgradeOptions, tag, exe string) error {
	info, err := os.Stat(exe)
	if err != nil {
		return err
	}
	asset := "shipwick_" + opts.goos + "_" + opts.goarch
	if opts.goos == "windows" {
		asset += ".exe"
	}

	// The staging file is opened first: a directory that cannot be written to
	// should be reported before anything is downloaded.
	staged := filepath.Join(filepath.Dir(exe), ".shipwick-new")
	f, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return notWritable(filepath.Dir(exe))
		}
		return fmt.Errorf("write %s: %w", staged, err)
	}
	// Removing the staging file is a no-op once it has been renamed into
	// place; on every other path it must not be left behind.
	defer os.Remove(staged)
	defer f.Close()

	base := opts.web + "/" + releaseRepo + "/releases/download/" + tag + "/"
	sums, err := opts.fetch(ctx, base+"checksums.txt", "", 1<<20)
	if err != nil {
		return fmt.Errorf("download the release checksums: %w", err)
	}
	want, ok := checksumFor(sums, asset)
	if !ok {
		return fmt.Errorf("release %s has no %s (looked in %s); nothing was changed", tag, asset, base+"checksums.txt")
	}

	resp, err := opts.get(ctx, base+asset, "")
	if err != nil {
		return fmt.Errorf("download %s: %w", base+asset, err)
	}
	defer resp.Body.Close()
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, sum), resp.Body); err != nil {
		return fmt.Errorf("download %s: %w", base+asset, err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		return fmt.Errorf("%s does not match the checksum published with release %s; nothing was changed\n  downloaded from %s", asset, tag, base+asset)
	}
	if err := f.Close(); err != nil {
		return fmt.Errorf("write %s: %w", staged, err)
	}
	if err := os.Chmod(staged, info.Mode().Perm()); err != nil {
		return fmt.Errorf("set permissions on %s: %w", staged, err)
	}
	if err := replaceExecutable(exe, staged); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return notWritable(filepath.Dir(exe))
		}
		return fmt.Errorf("replace %s: %w", exe, err)
	}
	return nil
}

// compareServer tells whether the server is behind the latest release, and
// how to upgrade it. It never fails the command: not being able to reach the
// server is one line, not an error.
func (c *cli) compareServer(ctx context.Context, latest string) {
	target, err := c.resolve()
	if err != nil {
		c.ui.Println(c.ui.Styled(ui.Dim, "The server was not checked: "+strings.SplitN(err.Error(), "\n", 2)[0]))
		return
	}
	// Health needs no token, so none is sent.
	cl, err := client.New(target.URL, "")
	if err != nil {
		c.ui.Println(c.ui.Styled(ui.Dim, "The server was not checked: "+err.Error()))
		return
	}
	health, err := cl.Health(ctx)
	var unreachable *client.UnreachableError
	switch {
	case errors.As(err, &unreachable):
		c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("The server at %s could not be reached; its version was not checked.", c.describeServer(cl.URL()))))
		return
	case err != nil:
		c.ui.Println(c.ui.Styled(ui.Dim, fmt.Sprintf("The server at %s did not answer as a Shipwick agent; its version was not checked.", c.describeServer(cl.URL()))))
		return
	}
	switch {
	case !parses(health.Version):
		c.ui.Println(fmt.Sprintf("The server runs a development build (%s).", health.Version))
	case version.Compare(health.Version, latest) < 0:
		c.ui.Println(fmt.Sprintf("The server runs %s; %s is available. On the server run:\n  %s", health.Version, latest, installerCommand))
	default:
		c.ui.Println(fmt.Sprintf("The server runs %s, the latest release.", health.Version))
	}
}

func parses(v string) bool {
	_, ok := version.Parse(v)
	return ok
}

// latestRelease returns the tag of the latest release, never a pre-release.
// GitHub's anonymous API allows 60 requests an hour per address, which shared
// networks exhaust; the website's /releases/latest redirect answers the same
// question without a limit.
func (o upgradeOptions) latestRelease(ctx context.Context) (string, error) {
	resp, err := o.get(ctx, o.api+"/repos/"+releaseRepo+"/releases/latest", "application/vnd.github+json")
	if err != nil {
		return "", fmt.Errorf("look up the latest release: %w", err)
	}
	defer resp.Body.Close()

	switch resp.StatusCode {
	case http.StatusOK:
		var rel struct {
			TagName    string `json:"tag_name"`
			Prerelease bool   `json:"prerelease"`
		}
		if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&rel); err != nil {
			return "", fmt.Errorf("look up the latest release: unexpected answer from %s: %w", o.api, err)
		}
		if tag, ok := releaseTag(rel.TagName); ok && !rel.Prerelease {
			return tag, nil
		}
	case http.StatusForbidden, http.StatusTooManyRequests:
	default:
		return "", fmt.Errorf("look up the latest release: GitHub answered HTTP %d for %s", resp.StatusCode, resp.Request.URL)
	}
	return o.latestReleaseFromRedirect(ctx)
}

func (o upgradeOptions) latestReleaseFromRedirect(ctx context.Context) (string, error) {
	url := o.web + "/" + releaseRepo + "/releases/latest"
	noFollow := *o.http
	noFollow.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "shipwick/"+version.Version)
	resp, err := noFollow.Do(req)
	if err != nil {
		return "", fmt.Errorf("look up the latest release: %w", err)
	}
	defer resp.Body.Close()

	location := resp.Header.Get("Location")
	if resp.StatusCode < 300 || resp.StatusCode > 399 || !strings.Contains(location, "/releases/tag/") {
		return "", fmt.Errorf("look up the latest release: %s did not point at a release (HTTP %d)", url, resp.StatusCode)
	}
	tag, ok := releaseTag(path.Base(location))
	if !ok {
		return "", fmt.Errorf("look up the latest release: %s points at %q, which is not a version", url, path.Base(location))
	}
	return tag, nil
}

// releaseTag accepts a tag that names a version, "v0.3.0".
func releaseTag(tag string) (string, bool) {
	tag = strings.TrimSpace(tag)
	_, ok := version.Parse(tag)
	return tag, ok && tag != ""
}

func (o upgradeOptions) get(ctx context.Context, url, accept string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "shipwick/"+version.Version)
	if accept != "" {
		req.Header.Set("Accept", accept)
	}
	resp, err := o.http.Do(req)
	if err != nil {
		return nil, err
	}
	if accept == "" && resp.StatusCode != http.StatusOK {
		resp.Body.Close()
		return nil, fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return resp, nil
}

// fetch reads a small text asset whole.
func (o upgradeOptions) fetch(ctx context.Context, url, accept string, limit int64) ([]byte, error) {
	resp, err := o.get(ctx, url, accept)
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	defer resp.Body.Close()
	data, err := io.ReadAll(io.LimitReader(resp.Body, limit))
	if err != nil {
		return nil, fmt.Errorf("%s: %w", url, err)
	}
	return data, nil
}

// checksumFor finds asset's SHA-256 in a checksums.txt as sha256sum and
// shasum write it: "<hex>  <name>", one per line, sometimes "*<name>".
func checksumFor(sums []byte, asset string) (string, bool) {
	for _, line := range strings.Split(string(sums), "\n") {
		fields := strings.Fields(line)
		if len(fields) != 2 || len(fields[0]) != sha256.Size*2 {
			continue
		}
		if strings.TrimPrefix(fields[1], "*") == asset {
			return strings.ToLower(fields[0]), true
		}
	}
	return "", false
}

// installMethod tells from the binary's real location whether a package
// manager owns it: Homebrew keeps everything under a Cellar, winget under its
// Packages directory. Both would be confused by a file they did not put there.
func installMethod(exe string) string {
	p := strings.ToLower(strings.ReplaceAll(exe, `\`, "/"))
	switch {
	case strings.Contains(p, "/cellar/shipwick/"), strings.Contains(p, "/homebrew/"), strings.Contains(p, "/linuxbrew/"):
		return "brew"
	case strings.Contains(p, "/winget/packages/"), strings.Contains(p, "/microsoft/winget/"):
		return "winget"
	}
	return ""
}

// staleExecutableName is where a Windows upgrade parks the binary it
// replaces: shipwick.exe becomes shipwick.old.exe.
func staleExecutableName(exe string) string {
	if strings.EqualFold(filepath.Ext(exe), ".exe") {
		exe = exe[:len(exe)-len(".exe")]
	}
	return exe + ".old.exe"
}

func notWritable(dir string) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("cannot write to %s: permission denied\n\nRun it from an administrator prompt, or download shipwick_windows_amd64.exe from\nhttps://github.com/%s/releases and replace the file yourself", dir, releaseRepo)
	}
	return fmt.Errorf("cannot write to %s: permission denied\n\nRun it as root: sudo shipwick upgrade\nor run the installer again: %s -s -- --cli", dir, installerCommand)
}
