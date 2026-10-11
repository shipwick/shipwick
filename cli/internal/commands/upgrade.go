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
	// machine is the architecture of the machine, which on Windows need not
	// be the one this binary was built for: see machineArch.
	machine string
	version string
	// cosign locates the program that verifies a release's signature, ""
	// when this machine has none; run runs it. See signature.go.
	cosign func() string
	run    func(ctx context.Context, argv []string, out io.Writer) error
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
	if o.machine == "" {
		// A test that names an architecture means the machine's too.
		if o.machine = o.goarch; o.machine == "" {
			o.machine = machineArch()
		}
	}
	if o.goarch == "" {
		o.goarch = runtime.GOARCH
	}
	if o.version == "" {
		o.version = version.Version
	}
	if o.cosign == nil {
		o.cosign = findCosign
	}
	if o.run == nil {
		o.run = runVerifier
	}
	return o
}

// upgradeCommands returns the upgrade, context commands.
func (c *cli) upgradeCommands() []*cobra.Command {
	return []*cobra.Command{c.upgradeCommand(), c.contextCommand()}
}

func (c *cli) upgradeCommand() *cobra.Command {
	var check bool
	var pinned string
	cmd := &cobra.Command{
		Use:   "upgrade",
		Short: "Replace this shipwick with the latest release",
		Long: `Replace this shipwick with the latest release.

The release is downloaded from GitHub and verified against its published
checksums before the binary is swapped. A shipwick installed with Homebrew or
winget is left to the package manager; the command tells you what to run.

The checksums are signed by the workflow that published the release. With
cosign installed (2.5 or later) the signature is verified first, and an
upgrade whose signature is not that workflow's changes nothing; without
cosign the command says that it was not checked.

--version installs exactly the release named, a pre-release included, in
place of whatever runs now — the way to try a release candidate, or to go
back to the release before. Without it, pre-releases are never picked.

The server is not upgraded by this command: the installer does that, on the
server, with access to Docker. If the server is behind, the command says so.`,
		Args: cobra.NoArgs,
		RunE: func(cmd *cobra.Command, _ []string) error {
			opts := c.upgrade.withDefaults()
			ctx := cmd.Context()
			if pinned != "" {
				tag, err := pinnedRelease(pinned)
				if err != nil {
					return err
				}
				if err := c.installRelease(ctx, opts, tag, check); err != nil {
					return err
				}
				c.ui.Println()
				c.compareServer(ctx, tag, true)
				return nil
			}
			latest, err := opts.latestRelease(ctx)
			if err != nil {
				return err
			}
			if err := c.upgradeBinary(ctx, opts, latest, check); err != nil {
				return err
			}
			c.ui.Println()
			c.compareServer(ctx, latest, false)
			return nil
		},
	}
	cmd.Flags().BoolVar(&check, "check", false, "report what is available and change nothing")
	cmd.Flags().StringVar(&pinned, "version", "", "install exactly this release, a pre-release included (default: the latest release)")
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
	return c.swapBinary(ctx, opts, latest, exe)
}

// installRelease puts release tag in place of the running binary, whatever
// that is: --version is a choice, not a comparison, so a development build
// and a newer release are replaced too. Only a binary that is that release
// already is left alone.
func (c *cli) installRelease(ctx context.Context, opts upgradeOptions, tag string, check bool) error {
	if parses(opts.version) && version.Compare(opts.version, tag) == 0 {
		c.ui.Println(fmt.Sprintf("shipwick %s is installed already.", tag))
		return nil
	}
	exe, err := opts.executable()
	if err != nil {
		return fmt.Errorf("locate the running shipwick: %w", err)
	}
	// A package manager installs its latest and nothing else; a file put in
	// its directory by hand would confuse it.
	if method := installMethod(exe); method != "" {
		manager := map[string]string{"brew": "Homebrew", "winget": "winget"}[method]
		c.ui.Println(fmt.Sprintf("shipwick %s was installed with %s and is left to it. To run %s, download it from\n%s/%s/releases/tag/%s and put it on your PATH ahead of %s's.", opts.version, manager, tag, opts.web, releaseRepo, tag, manager))
		return nil
	}
	if check {
		c.ui.Println(fmt.Sprintf("shipwick %s is installed.\n\nInstall %s with: shipwick upgrade --version %s", opts.version, tag, tag))
		return nil
	}
	return c.swapBinary(ctx, opts, tag, exe)
}

// swapBinary downloads release tag over exe and says what happened.
func (c *cli) swapBinary(ctx context.Context, opts upgradeOptions, tag, exe string) error {
	c.ui.Progress("Downloading shipwick %s…", tag)
	signature, err := c.replaceBinary(ctx, opts, tag, exe)
	c.ui.Done()
	if err != nil {
		return err
	}
	if parses(opts.version) && version.Compare(opts.version, tag) < 0 {
		c.ui.Success("Upgraded shipwick %s → %s", opts.version, tag)
	} else {
		c.ui.Success("Installed shipwick %s in place of %s", tag, opts.version)
	}
	c.ui.Println(c.ui.Styled(ui.Dim, "  "+exe))
	c.ui.Println(c.ui.Styled(ui.Dim, "  "+signature.describe(tag)))
	return nil
}

// pinnedRelease is the tag --version names: a version, with or without its
// "v". It goes into URLs and into cosign's identity, so nothing else passes.
func pinnedRelease(s string) (string, error) {
	tag := strings.TrimSpace(s)
	if !strings.HasPrefix(tag, "v") {
		tag = "v" + tag
	}
	if _, ok := version.Parse(tag); !ok || strings.ContainsAny(tag, " '\"/") {
		return "", fmt.Errorf("--version %q is not a release version; releases look like v0.9.0 or v0.9.0-rc.1", s)
	}
	return tag, nil
}

// replaceBinary downloads the release asset for this platform next to the
// running binary, verifies it against checksums.txt — and checksums.txt
// against the release's signature, where cosign is installed — and renames it
// into place. Nothing about the old binary changes until the new one is
// verified.
func (c *cli) replaceBinary(ctx context.Context, opts upgradeOptions, tag, exe string) (signatureState, error) {
	info, err := os.Stat(exe)
	if err != nil {
		return 0, err
	}
	asset := releaseAsset(opts.goos, opts.machine)

	// The staging file is opened first: a directory that cannot be written to
	// should be reported before anything is downloaded.
	staged := filepath.Join(filepath.Dir(exe), ".shipwick-new")
	f, err := os.OpenFile(staged, os.O_CREATE|os.O_WRONLY|os.O_TRUNC, 0o755)
	if err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return 0, notWritable(filepath.Dir(exe), releaseAsset(opts.goos, opts.machine))
		}
		return 0, fmt.Errorf("write %s: %w", staged, err)
	}
	// Removing the staging file is a no-op once it has been renamed into
	// place; on every other path it must not be left behind.
	defer os.Remove(staged)
	defer f.Close()

	base := opts.web + "/" + releaseRepo + "/releases/download/" + tag + "/"
	sums, err := opts.fetch(ctx, base+"checksums.txt", "", 1<<20)
	if err != nil {
		// A release that is not there has no files at all; said as such,
		// rather than as a file that is missing.
		var status httpStatus
		if errors.As(err, &status) && status == http.StatusNotFound {
			return 0, fmt.Errorf("release %s was not found at %s; the releases are listed at %s/%s/releases", tag, base+"checksums.txt", opts.web, releaseRepo)
		}
		return 0, fmt.Errorf("download the release checksums: %w", err)
	}
	signature, _, err := opts.checkSignature(ctx, base, tag, sums)
	if err != nil {
		return 0, err
	}
	want, ok := checksumFor(sums, asset)
	if !ok && opts.machine != opts.goarch {
		// A release from before the machine's architecture was built for
		// still has the build this binary is.
		if w, found := checksumFor(sums, releaseAsset(opts.goos, opts.goarch)); found {
			asset, want, ok = releaseAsset(opts.goos, opts.goarch), w, true
		}
	}
	if !ok {
		return 0, fmt.Errorf("release %s has no %s (looked in %s); nothing was changed", tag, asset, base+"checksums.txt")
	}

	resp, err := opts.get(ctx, base+asset, "")
	if err != nil {
		return 0, fmt.Errorf("download %s: %w", base+asset, err)
	}
	defer resp.Body.Close()
	sum := sha256.New()
	if _, err := io.Copy(io.MultiWriter(f, sum), resp.Body); err != nil {
		return 0, fmt.Errorf("download %s: %w", base+asset, err)
	}
	if got := hex.EncodeToString(sum.Sum(nil)); got != want {
		return 0, fmt.Errorf("%s does not match the checksum published with release %s; nothing was changed\n  downloaded from %s", asset, tag, base+asset)
	}
	if err := f.Close(); err != nil {
		return 0, fmt.Errorf("write %s: %w", staged, err)
	}
	if err := os.Chmod(staged, info.Mode().Perm()); err != nil {
		return 0, fmt.Errorf("set permissions on %s: %w", staged, err)
	}
	if err := replaceExecutable(exe, staged); err != nil {
		if errors.Is(err, fs.ErrPermission) {
			return 0, notWritable(filepath.Dir(exe), releaseAsset(opts.goos, opts.machine))
		}
		return 0, fmt.Errorf("replace %s: %w", exe, err)
	}
	return signature, nil
}

// compareServer tells whether the server is behind the latest release — or,
// pinned, behind the release --version named — and how to upgrade it. It
// never fails the command: not being able to reach the server is one line,
// not an error.
func (c *cli) compareServer(ctx context.Context, latest string, pinned bool) {
	installer, standing := installerCommand, ", the latest release"
	if pinned {
		installer, standing = strings.Replace(installerCommand, "| sh", "| SHIPWICK_VERSION="+latest+" sh", 1), ""
	}
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
		c.ui.Println(fmt.Sprintf("The server runs %s; %s is available. On the server run:\n  %s", health.Version, latest, installer))
	case version.Compare(health.Version, latest) > 0 && !pinned:
		c.ui.Println(fmt.Sprintf("The server runs %s, ahead of the latest release (%s).", health.Version, latest))
	default:
		c.ui.Println(fmt.Sprintf("The server runs %s%s.", health.Version, standing))
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
		return nil, httpStatus(resp.StatusCode)
	}
	return resp, nil
}

// httpStatus is an answer that was not 200, for a caller that tells a 404
// from the rest.
type httpStatus int

func (s httpStatus) Error() string { return fmt.Sprintf("HTTP %d", int(s)) }

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

// releaseAsset is the name of the CLI's build for a platform among the
// release's files (scripts/build-release.sh).
func releaseAsset(goos, goarch string) string {
	asset := "shipwick_" + goos + "_" + goarch
	if goos == "windows" {
		asset += ".exe"
	}
	return asset
}

func notWritable(dir, asset string) error {
	if runtime.GOOS == "windows" {
		return fmt.Errorf("cannot write to %s: permission denied\n\nRun it from an administrator prompt, or download %s from\nhttps://github.com/%s/releases and replace the file yourself", dir, asset, releaseRepo)
	}
	return fmt.Errorf("cannot write to %s: permission denied\n\nRun it as root: sudo shipwick upgrade\nor run the installer again: %s -s -- --cli", dir, installerCommand)
}
