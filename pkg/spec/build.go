package spec

import (
	"fmt"
	"path"
	"strings"

	"go.yaml.in/yaml/v3"
)

// LocalImageHost is the registry host of images that never see a registry:
// built where `shipwick deploy` runs and sent to the server, which loads them.
// No such host exists, so nothing can pull from it, and an image under it is
// recognizable as one the server must already hold.
const LocalImageHost = "shipwick.local"

// DefaultDockerfile is the Dockerfile a build uses when deploy.yaml names none.
const DefaultDockerfile = "Dockerfile"

// IsLocalImage reports whether ref names an image under LocalImageHost.
func IsLocalImage(ref string) bool {
	return strings.HasPrefix(ref, LocalImageHost+"/")
}

// LocalImagePrefix is what every local image of the application starts with:
// "shipwick.local/<name>:". An image loaded for one application cannot be
// mistaken for another's.
func LocalImagePrefix(app string) string {
	return LocalImageHost + "/" + app + ":"
}

// buildRaw accepts `build: .` and `build: {context: ., dockerfile: Dockerfile}`.
type buildRaw struct {
	Context    string `yaml:"context"`
	Dockerfile string `yaml:"dockerfile"`
}

func (b *buildRaw) UnmarshalYAML(node *yaml.Node) error {
	for node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	switch node.Kind {
	case yaml.ScalarNode:
		b.Context = node.Value
		return nil
	case yaml.MappingNode:
		type plain buildRaw
		return node.Decode((*plain)(b))
	}
	return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: expected a path or a map with context and dockerfile, not a %s",
		node.Line, strings.TrimPrefix(node.ShortTag(), "!!"))}}
}

// validateBuild checks the build context and Dockerfile paths. Both are paths
// on the developer's machine, relative to deploy.yaml, and end up as arguments
// of `docker build` there: nothing absolute, nothing that climbs out of the
// project.
func (r raw) validateBuild(verr *ValidationError) *Build {
	if r.Build.Context == "" && r.Build.Dockerfile == "" {
		return nil
	}
	b := &Build{Dockerfile: DefaultDockerfile}

	if context := strings.TrimSpace(r.Build.Context); context == "" {
		verr.add("build.context", "is required", ".")
	} else {
		cleaned, err := validateBuildPath(context)
		if err != nil {
			verr.add("build.context", err.Error(), ". for the directory of deploy.yaml, or a folder in it such as api")
		}
		b.Context = cleaned
	}

	if dockerfile := strings.TrimSpace(r.Build.Dockerfile); dockerfile != "" {
		cleaned, err := validateBuildPath(dockerfile)
		if err != nil {
			verr.add("build.dockerfile", err.Error(), "Dockerfile, or a file in the context such as docker/Dockerfile.prod")
		}
		b.Dockerfile = cleaned
	}

	if r.Static != "" {
		verr.add("build", "cannot be combined with static: a static application has no image to build", "one of build and static")
	}
	// The CLI fills in image with the reference the server answered; any
	// other image next to build would leave two answers to "what runs".
	if image := strings.TrimSpace(r.Image); image != "" && !IsLocalImage(image) {
		verr.add("image", "is built here; remove image or build", "build: . alone, or image: alone")
	}
	return b
}

// validateBuildPath accepts a relative path inside the project and returns it
// cleaned, with forward slashes: what `docker build` takes on every platform.
func validateBuildPath(p string) (string, error) {
	if strings.ContainsAny(p, "\x00\r\n") {
		return "", fmt.Errorf("invalid value %q: must not contain control characters", p)
	}
	slashed := strings.ReplaceAll(p, `\`, "/")
	if strings.HasPrefix(slashed, "/") || strings.HasPrefix(slashed, "~") || isDrivePath(slashed) {
		return "", fmt.Errorf("invalid value %q: must be relative to deploy.yaml", p)
	}
	cleaned := path.Clean(slashed)
	if cleaned == ".." || strings.HasPrefix(cleaned, "../") {
		return "", fmt.Errorf("invalid value %q: must stay inside the directory of deploy.yaml", p)
	}
	return cleaned, nil
}

// isDrivePath recognizes a Windows absolute path such as C:/x or C:\x.
func isDrivePath(p string) bool {
	return len(p) >= 2 && p[1] == ':' &&
		(('a' <= p[0] && p[0] <= 'z') || ('A' <= p[0] && p[0] <= 'Z'))
}
