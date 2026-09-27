package store

import (
	"context"
	"strings"
	"time"

	"github.com/shipwick/shipwick/pkg/spec"
)

// StaticFiles is what a static deployment serves: the uploaded archive by its
// digest, and what was in it.
type StaticFiles struct {
	Digest string // "sha256:<64 hex characters>"
	Files  int
	Bytes  int64
}

// CreateStaticDeployment is CreateDeploymentFrom for a static application. It
// has no image to derive a version from; the version is the digest's first
// twelve hex characters, the way an image digest is shortened.
func (s *Store) CreateStaticDeployment(ctx context.Context, app spec.App, kind string, sourceID *int64, actor string, files StaticFiles, now time.Time) (Deployment, error) {
	return s.insertDeployment(ctx, Deployment{
		Application:  app.Name,
		Version:      StaticVersion(files.Digest),
		Spec:         app,
		Kind:         kind,
		SourceID:     sourceID,
		Actor:        actor,
		StaticDigest: files.Digest,
		StaticFiles:  files.Files,
		StaticBytes:  files.Bytes,
	}, now)
}

// StaticVersion is the human-facing version of a static deployment: the
// first twelve hex characters of its digest.
func StaticVersion(digest string) string {
	hex := strings.TrimPrefix(digest, "sha256:")
	if len(hex) > 12 {
		hex = hex[:12]
	}
	return hex
}
