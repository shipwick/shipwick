package spec

import (
	"errors"
	"strings"
	"testing"
)

func TestBuild(t *testing.T) {
	tests := []struct {
		name           string
		config         string
		wantContext    string
		wantDockerfile string
	}{
		{"a dot", "name: a\nbuild: .\n", ".", "Dockerfile"},
		{"a folder", "name: a\nbuild: api\n", "api", "Dockerfile"},
		{"a map", "name: a\nbuild:\n  context: services/api\n  dockerfile: docker/Dockerfile.prod\n", "services/api", "docker/Dockerfile.prod"},
		{"cleaned", "name: a\nbuild: ./api/../api/\n", "api", "Dockerfile"},
		{"backslashes become slashes", "name: a\nbuild: {context: .\\api, dockerfile: build\\Dockerfile}\n", "api", "build/Dockerfile"},
		{"with the image the CLI fills in", "name: a\nbuild: .\nimage: shipwick.local/a:20260927-153000-a1b2\n", ".", "Dockerfile"},
		{"only a dockerfile is built where the file is", "name: a\nbuild:\n  dockerfile: docker/Dockerfile.prod\n", ".", "docker/Dockerfile.prod"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			app, err := Parse([]byte(tt.config))
			if err != nil {
				t.Fatalf("Parse: %v", err)
			}
			if app.Build == nil || app.Build.Context != tt.wantContext || app.Build.Dockerfile != tt.wantDockerfile {
				t.Errorf("Build = %+v, want %s (%s)", app.Build, tt.wantContext, tt.wantDockerfile)
			}
		})
	}

	app, err := Parse([]byte("name: a\nimage: nginx\n"))
	if err != nil || app.Build != nil {
		t.Errorf("without build: Build = %+v, err = %v", app.Build, err)
	}
}

func TestBuildValidationErrors(t *testing.T) {
	tests := []struct {
		name      string
		config    string
		wantField string
		wantMsg   string
	}{
		{"absolute context", "name: a\nbuild: /srv/app\n", "build.context", "relative"},
		{"windows drive", "name: a\nbuild: C:\\app\n", "build.context", "relative"},
		{"home", "name: a\nbuild: ~/app\n", "build.context", "relative"},
		{"climbs out", "name: a\nbuild: ../other\n", "build.context", "inside"},
		{"climbs out after cleaning", "name: a\nbuild: api/../..\n", "build.context", "inside"},
		{"NUL", "name: a\nbuild: \"a\\x00b\"\n", "build.context", "control characters"},
		{"dockerfile outside the file's directory", "name: a\nbuild:\n  dockerfile: ../Dockerfile\n", "build.dockerfile", "inside"},
		{"dockerfile outside the context", "name: a\nbuild:\n  context: .\n  dockerfile: ../Dockerfile\n", "build.dockerfile", "inside"},
		{"absolute dockerfile", "name: a\nbuild:\n  context: .\n  dockerfile: /etc/Dockerfile\n", "build.dockerfile", "relative"},
		{"with static", "name: a\nbuild: .\nstatic: dist\n", "build", "cannot be combined with static"},
		{"with a registry image", "name: a\nbuild: .\nimage: ghcr.io/company/a:1.0\n", "image", "is built here; remove image or build"},
		{"not a path or a map", "name: a\nbuild: [.]\n", "line 2", "expected a path or a map"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			_, err := Parse([]byte(tt.config))
			var verr *ValidationError
			if !errors.As(err, &verr) {
				t.Fatalf("expected *ValidationError, got %v", err)
			}
			for _, f := range verr.Fields {
				if f.Field == tt.wantField && strings.Contains(f.Message, tt.wantMsg) {
					return
				}
			}
			t.Errorf("no error for field %q containing %q; got %+v", tt.wantField, tt.wantMsg, verr.Fields)
		})
	}
}

func TestBuildDoesNotRequireAnImage(t *testing.T) {
	_, err := Parse([]byte("name: a\nbuild: .\n"))
	if err != nil {
		t.Fatalf("build without image must be valid: %v", err)
	}
	_, err = Parse([]byte("name: a\n"))
	var verr *ValidationError
	if !errors.As(err, &verr) || !strings.Contains(verr.Error(), "image") {
		t.Errorf("without build or static, image is still required: %v", err)
	}
}

func TestLocalImages(t *testing.T) {
	if !IsLocalImage("shipwick.local/a:20260927-153000-a1b2") {
		t.Error("an image under shipwick.local is local")
	}
	for _, ref := range []string{"ghcr.io/company/a:1.0", "shipwick.local", "shipwick.localhost/a:1", "nginx"} {
		if IsLocalImage(ref) {
			t.Errorf("%q is not local", ref)
		}
	}
	if got := LocalImagePrefix("my-api"); got != "shipwick.local/my-api:" {
		t.Errorf("LocalImagePrefix = %q", got)
	}
	if err := ValidateImage("shipwick.local/my-api:20260927-153000-a1b2"); err != nil {
		t.Errorf("a local reference must be a valid image reference: %v", err)
	}
}
