package commands

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/shipwick/shipwick/pkg/version"
)

// A release's checksums say that a file is the one the release lists. Its
// signature says who wrote the list: checksums.txt is signed by the workflow
// that published the release, with a certificate Sigstore issued to that
// workflow run and to nothing else (.github/workflows/release.yml). Whoever
// can replace a release's files can replace its checksums with them; they
// cannot make that signature.
//
// Verifying it is cosign's work. The CLI does not carry a Sigstore verifier:
// the libraries for it are a dependency out of proportion to a CLI, taken to save
// installing a program the people who want this check already have. So cosign
// is run when this machine has it, and its absence is said, not hidden.

const (
	// releaseSignature is the Sigstore bundle of checksums.txt among the
	// release's files: signature, certificate and transparency log entry.
	releaseSignature = bundleChecksums + ".sigstore.json"
	// signatureIssuer is who vouches for the workflow's identity: GitHub
	// Actions' token service.
	signatureIssuer = "https://token.actions.githubusercontent.com"
	// firstSignedRelease is the first release that was signed. One from then
	// on that has no signature has had it taken away.
	firstSignedRelease = "v0.8.0"
)

// signatureIdentity is the identity the certificate must name: the release
// workflow of this repository, run for the tag. A signature made for another
// tag does not verify, so one release's files cannot be passed off as
// another's.
func signatureIdentity(tag string) string {
	return "https://github.com/" + releaseRepo + "/.github/workflows/release.yml@refs/tags/" + tag
}

// signatureState is what became of a release's signature.
type signatureState int

const (
	// signatureUnchecked: this machine has no cosign.
	signatureUnchecked signatureState = iota
	// signatureAbsent: a release from before releases were signed.
	signatureAbsent
	signatureVerified
)

// describe is the one line that tells the user which of the three it was.
func (s signatureState) describe(tag string) string {
	switch s {
	case signatureVerified:
		return fmt.Sprintf("Release %s is signed by the release workflow of github.com/%s (verified with cosign).", tag, releaseRepo)
	case signatureAbsent:
		return fmt.Sprintf("Release %s has no signature: releases before %s were not signed.", tag, strings.TrimPrefix(firstSignedRelease, "v"))
	}
	return "The release's signature was not checked: cosign is not installed on this machine."
}

// findCosign is the cosign on this machine's PATH, "" when there is none.
func findCosign() string {
	path, err := exec.LookPath("cosign")
	if err != nil {
		return ""
	}
	return path
}

// runVerifier runs a program on this machine with the given argv, its output
// going to out. Never a shell.
func runVerifier(ctx context.Context, argv []string, out io.Writer) error {
	cmd := exec.CommandContext(ctx, argv[0], argv[1:]...)
	cmd.Stdout, cmd.Stderr = out, out
	return cmd.Run()
}

// checkSignature verifies sums, the checksums.txt of release tag downloaded
// from base, against the release's signature, and returns the signature as
// downloaded (nil when the release has none). An error means that the
// release must not be used: the signature did not verify, or a release that
// should have one does not.
func (o upgradeOptions) checkSignature(ctx context.Context, base, tag string, sums []byte) (signatureState, []byte, error) {
	cosign := o.cosign()
	// Asked for as a download of any type, so that a missing file is an
	// answer to read and not an error.
	resp, err := o.get(ctx, base+releaseSignature, "application/octet-stream")
	if err != nil {
		return 0, nil, fmt.Errorf("download the signature of release %s: %w", tag, err)
	}
	defer resp.Body.Close()
	switch resp.StatusCode {
	case http.StatusOK:
	case http.StatusNotFound:
		if version.Compare(tag, firstSignedRelease) < 0 {
			return signatureAbsent, nil, nil
		}
		if cosign == "" {
			return signatureUnchecked, nil, nil
		}
		return 0, nil, fmt.Errorf("release %s has no signature (%s), and every release since %s has one; nothing was changed", tag, releaseSignature, strings.TrimPrefix(firstSignedRelease, "v"))
	default:
		return 0, nil, fmt.Errorf("download the signature of release %s: HTTP %d for %s", tag, resp.StatusCode, base+releaseSignature)
	}
	signature, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return 0, nil, fmt.Errorf("download the signature of release %s: %w", tag, err)
	}
	if cosign == "" {
		return signatureUnchecked, signature, nil
	}

	// cosign reads files.
	dir, err := os.MkdirTemp("", "shipwick-signature-")
	if err != nil {
		return 0, nil, err
	}
	defer os.RemoveAll(dir)
	sumsFile, signatureFile := filepath.Join(dir, bundleChecksums), filepath.Join(dir, releaseSignature)
	if err := os.WriteFile(sumsFile, sums, 0o600); err != nil {
		return 0, nil, err
	}
	if err := os.WriteFile(signatureFile, signature, 0o600); err != nil {
		return 0, nil, err
	}
	var out bytes.Buffer
	err = o.run(ctx, []string{cosign, "verify-blob",
		"--bundle", signatureFile,
		"--certificate-identity", signatureIdentity(tag),
		"--certificate-oidc-issuer", signatureIssuer,
		sumsFile}, &out)
	if err != nil {
		lines := strings.Split(strings.TrimSpace(out.String()), "\n")
		said := strings.TrimSpace(lines[len(lines)-1])
		if said == "" {
			said = err.Error()
		}
		return 0, nil, fmt.Errorf("the signature of release %s's checksums did not verify as one made by the release workflow of github.com/%s for %s; nothing was changed\n  cosign: %s\n  cosign 2.4 or later reads this signature; an older one fails here whatever the file", tag, releaseRepo, tag, said)
	}
	return signatureVerified, signature, nil
}
