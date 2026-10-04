package commands

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// fakeCosign stands in for cosign: it records what it was asked to verify
// and answers as told.
type fakeCosign struct {
	calls [][]string
	// verified is what the file it was given held, and signature the bundle.
	verified, signature string
	// refuse makes it answer as cosign does to a signature that is not the
	// workflow's.
	refuse bool
}

func (k *fakeCosign) path() string { return "/usr/local/bin/cosign" }

func (k *fakeCosign) run(_ context.Context, argv []string, out io.Writer) error {
	k.calls = append(k.calls, argv)
	if file, err := os.ReadFile(argv[len(argv)-1]); err == nil {
		k.verified = string(file)
	}
	if i := slices.Index(argv, "--bundle"); i >= 0 {
		file, _ := os.ReadFile(argv[i+1])
		k.signature = string(file)
	}
	if k.refuse {
		fmt.Fprintln(out, "Error: verifying blob: ...")
		fmt.Fprintln(out, `error during command execution: failed to verify certificate identity: no matching CertificateIdentity found`)
		return errors.New("exit status 1")
	}
	fmt.Fprintln(out, "Verified OK")
	return nil
}

// signedGitHub is a fake GitHub whose latest release is tag, with a
// signature unless the test takes it away.
func signedGitHub(t *testing.T, tag string) *fakeGitHub {
	g := newFakeGitHub(t)
	g.tag = tag
	g.extra = map[string][]byte{releaseSignature: []byte(`{"mediaType":"application/vnd.dev.sigstore.bundle.v0.3+json"}`)}
	return g
}

func TestUpgradeVerifiesTheReleasesSignatureWhenCosignIsInstalled(t *testing.T) {
	g, cosign := signedGitHub(t, "v0.8.1"), &fakeCosign{}
	exe := installed(t)
	f := upgradeAgent(t, g, exe, "v0.8.0")
	f.upgrade.cosign, f.upgrade.run = cosign.path, cosign.run

	out, _, err := f.run(t.TempDir(), "upgrade")
	if err != nil {
		t.Fatalf("upgrade: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"Upgraded shipwick v0.8.0 → v0.8.1", "Release v0.8.1 is signed by the release workflow of github.com/shipwick/shipwick"})
	if len(cosign.calls) != 1 {
		t.Fatalf("cosign ran %d times, want once: %v", len(cosign.calls), cosign.calls)
	}
	argv := cosign.calls[0]
	want := []string{"/usr/local/bin/cosign", "verify-blob",
		"--certificate-identity", "https://github.com/shipwick/shipwick/.github/workflows/release.yml@refs/tags/v0.8.1",
		"--certificate-oidc-issuer", "https://token.actions.githubusercontent.com"}
	for _, arg := range want {
		if !slices.Contains(argv, arg) {
			t.Errorf("cosign was run as %v, without %q", argv, arg)
		}
	}
	if !strings.HasSuffix(cosign.verified, "  shipwick_linux_amd64\n") {
		t.Errorf("cosign was given %q, want the release's checksums.txt", cosign.verified)
	}
	if cosign.signature != string(g.extra[releaseSignature]) {
		t.Errorf("cosign was given the signature %q, want the release's", cosign.signature)
	}
	if got, _ := os.ReadFile(exe); string(got) != string(g.binary) {
		t.Errorf("the binary was not replaced, still holds %q", got)
	}
	if left, _ := filepath.Glob(filepath.Join(os.TempDir(), "shipwick-signature-*")); len(left) != 0 {
		t.Errorf("the files cosign read were left behind: %v", left)
	}
}

func TestUpgradeChangesNothingWhenTheSignatureIsNotTheWorkflows(t *testing.T) {
	g, cosign := signedGitHub(t, "v0.8.1"), &fakeCosign{refuse: true}
	exe := installed(t)
	f := upgradeAgent(t, g, exe, "v0.8.0")
	f.upgrade.cosign, f.upgrade.run = cosign.path, cosign.run

	_, _, err := f.run(t.TempDir(), "upgrade")
	if err == nil {
		t.Fatal("an upgrade whose signature does not verify must fail")
	}
	for _, want := range []string{"did not verify", "release workflow of github.com/shipwick/shipwick for v0.8.1", "nothing was changed", "no matching CertificateIdentity", "cosign 2.4 or later"} {
		if !strings.Contains(err.Error(), want) {
			t.Errorf("the error does not say %q:\n%v", want, err)
		}
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary v0.2.0\n" {
		t.Errorf("the binary must be untouched, holds %q", got)
	}
	if strings.Contains(strings.Join(g.requests, " "), "/shipwick_linux_amd64") {
		t.Errorf("the binary of a release that failed its signature was downloaded: %v", g.requests)
	}
}

func TestUpgradeRefusesAReleaseWhoseSignatureWasTakenAway(t *testing.T) {
	g, cosign := signedGitHub(t, "v0.8.1"), &fakeCosign{}
	g.extra = nil
	exe := installed(t)
	f := upgradeAgent(t, g, exe, "v0.8.0")
	f.upgrade.cosign, f.upgrade.run = cosign.path, cosign.run

	_, _, err := f.run(t.TempDir(), "upgrade")
	if err == nil || !strings.Contains(err.Error(), "release v0.8.1 has no signature") || !strings.Contains(err.Error(), "every release since 0.8.0 has one") {
		t.Fatalf("err = %v, want a refusal that names the missing signature", err)
	}
	if len(cosign.calls) != 0 {
		t.Errorf("there was nothing for cosign to verify, it ran: %v", cosign.calls)
	}
	if got, _ := os.ReadFile(exe); string(got) != "old binary v0.2.0\n" {
		t.Errorf("the binary must be untouched, holds %q", got)
	}
}

func TestUpgradeSaysWhenTheSignatureWasNotChecked(t *testing.T) {
	for _, signed := range []bool{true, false} {
		g := signedGitHub(t, "v0.8.1")
		if !signed {
			g.extra = nil
		}
		exe := installed(t)
		f := upgradeAgent(t, g, exe, "v0.8.0")

		out, _, err := f.run(t.TempDir(), "upgrade")
		if err != nil {
			t.Fatalf("signed %v: without cosign the upgrade goes by the checksums: %v", signed, err)
		}
		assertInOrder(t, out, []string{"Upgraded shipwick v0.8.0 → v0.8.1", "The release's signature was not checked: cosign is not installed on this machine."})
		if got, _ := os.ReadFile(exe); string(got) != string(g.binary) {
			t.Errorf("signed %v: the binary was not replaced, still holds %q", signed, got)
		}
	}
}

func TestServerBundleVerifiesTheSignatureAndCarriesIt(t *testing.T) {
	release, docker, cosign := newFakeRelease(), &bundleDocker{}, &fakeCosign{}
	release.tag = "v0.8.0"
	release.assets[releaseSignature] = "the signature"
	f := newFakeAgent(t)
	f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}
	f.upgrade.cosign, f.upgrade.run = cosign.path, cosign.run
	dir := t.TempDir()

	out, _, err := f.run(dir, "server", "bundle")
	if err != nil {
		t.Fatalf("server bundle: %v\n%s", err, out)
	}
	assertInOrder(t, out, []string{"match its checksums", "Release v0.8.0 is signed by the release workflow of github.com/shipwick/shipwick", "Wrote shipwick-v0.8.0-linux-amd64.tar.gz"})
	if len(cosign.calls) != 1 || !slices.Contains(cosign.calls[0], signatureIdentity("v0.8.0")) {
		t.Errorf("cosign calls = %v, want one for the tag of the bundle's release", cosign.calls)
	}
	files, _ := readBundle(t, filepath.Join(dir, "shipwick-v0.8.0-linux-amd64.tar.gz"))
	top := "shipwick-v0.8.0-linux-amd64/"
	if files[top+releaseSignature] != "the signature" {
		t.Errorf("the bundle does not carry the release's signature: %q", files[top+releaseSignature])
	}
	if files[top+"checksums.txt"] != cosign.verified {
		t.Errorf("cosign verified other checksums than the bundle holds:\n%q\n%q", cosign.verified, files[top+"checksums.txt"])
	}
}

func TestServerBundleWritesNothingWhenTheSignatureIsNotTheWorkflows(t *testing.T) {
	release, docker, cosign := newFakeRelease(), &bundleDocker{}, &fakeCosign{refuse: true}
	release.tag = "v0.8.0"
	release.assets[releaseSignature] = "somebody else's"
	f := newFakeAgent(t)
	f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}
	f.upgrade.cosign, f.upgrade.run = cosign.path, cosign.run
	dir := t.TempDir()

	_, _, err := f.run(dir, "server", "bundle")
	if err == nil || !strings.Contains(err.Error(), "did not verify") {
		t.Fatalf("err = %v, want a refusal that names the signature", err)
	}
	if len(docker.calls) != 0 {
		t.Errorf("nothing should be pulled for a release that failed its signature: %v", docker.calls)
	}
	if left, _ := os.ReadDir(dir); len(left) != 0 {
		t.Errorf("files were left behind: %v", left)
	}
}

func TestServerBundleSaysWhatBecameOfTheSignature(t *testing.T) {
	tests := []struct {
		name, tag string
		signed    bool
		cosign    bool
		want      string
		carried   bool
	}{
		{"a release from before releases were signed", "v0.6.0", false, true, "Release v0.6.0 has no signature: releases before 0.8.0 were not signed.", false},
		{"a signed release on a machine without cosign", "v0.8.0", true, false, "The release's signature was not checked: cosign is not installed on this machine.", true},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			release, docker, cosign := newFakeRelease(), &bundleDocker{}, &fakeCosign{}
			release.tag = tt.tag
			if tt.signed {
				release.assets[releaseSignature] = "the signature"
			}
			f := newFakeAgent(t)
			f.upgrade, f.build = release.serve(t), buildTools{run: docker.run}
			if tt.cosign {
				f.upgrade.cosign, f.upgrade.run = cosign.path, cosign.run
			}
			dir := t.TempDir()

			out, _, err := f.run(dir, "server", "bundle")
			if err != nil {
				t.Fatalf("server bundle: %v\n%s", err, out)
			}
			if !strings.Contains(out, tt.want) {
				t.Errorf("the output does not say %q:\n%s", tt.want, out)
			}
			name := "shipwick-" + tt.tag + "-linux-amd64"
			files, _ := readBundle(t, filepath.Join(dir, name+".tar.gz"))
			if _, carried := files[name+"/"+releaseSignature]; carried != tt.carried {
				t.Errorf("the bundle carries a signature: %v, want %v", carried, tt.carried)
			}
			if len(cosign.calls) != 0 {
				t.Errorf("cosign had nothing to verify, it ran: %v", cosign.calls)
			}
		})
	}
}
