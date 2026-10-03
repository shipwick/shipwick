package spec

import (
	"fmt"
	"path"
	"reflect"
	"strings"

	"go.yaml.in/yaml/v3"
)

// staticExclusiveMessage is what every container-only field gets when it is
// set next to `static`.
const staticExclusiveMessage = "does not apply to a static application: the proxy serves the files, there is no container"

// validateStatic checks a static application: the folder, and that nothing
// meant for a container is set next to it.
func (r raw) validateStatic(verr *ValidationError, app App) *Static {
	if r.Static.Dir == "" {
		if r.Static.Fallback != "" {
			verr.add("static.dir", "is required: the folder the fallback page is in", "dist")
		}
		return nil
	}
	dir, problem := cleanStaticDir(r.Static.Dir)
	if problem != "" {
		verr.add("static", problem, "dist/, build/, out/ — a folder relative to deploy.yaml")
	}
	if app.Domain == "" {
		verr.add("domain", "is required for a static application: the proxy serves the files at it", "example.com")
	}

	// The proxy serves the files itself; anything that describes a container
	// has nothing to apply to, and a file that names both is a file whose
	// author expects one of them to happen.
	exclusive := []struct {
		field string
		set   bool
	}{
		{"image", r.Image != ""},
		{"build", r.Build.Context != "" || r.Build.Dockerfile != ""},
		{"port", r.Port != nil},
		{"replicas", r.Replicas != nil && *r.Replicas != DefaultReplicas},
		{"env", len(r.Env) > 0},
		{"health", r.Health != nil},
		{"resources", r.Resources.CPU != "" || r.Resources.Memory != ""},
		{"volumes", len(r.Volumes) > 0},
		{"publish", len(r.Publish) > 0},
		{"entrypoint", len(r.Entrypoint) > 0},
		{"command", len(r.Command) > 0},
		{"user", r.User != ""},
		{"logging", r.Logging != nil},
		{"pre_deploy", r.PreDeploy != nil},
		{"jobs", len(r.Jobs) > 0},
	}
	for _, f := range exclusive {
		if f.set {
			verr.add(f.field, staticExclusiveMessage, "")
		}
	}
	return &Static{Dir: dir, Fallback: r.validateFallback(verr)}
}

// validateFallback checks the file answered for paths that name no file. The
// proxy looks it up in the folder and its configuration names it, hence the
// strict alphabet.
func (r raw) validateFallback(verr *ValidationError) string {
	f := strings.TrimSpace(r.Static.Fallback)
	if f == "" {
		return ""
	}
	const example = "index.html, 200.html, app/index.html — a file in the folder"
	switch {
	case strings.HasPrefix(f, "/"):
		verr.add("static.fallback", fmt.Sprintf("invalid value %q: must be relative to the folder", f), example)
	case len(f) > MaxPathBytes:
		verr.add("static.fallback", fmt.Sprintf("invalid value: longer than %d characters", MaxPathBytes), example)
	case validateURLPath("/"+f) != nil:
		verr.add("static.fallback", fmt.Sprintf("invalid value %q: name a file inside the folder, with letters, digits, dots, dashes, underscores and tildes between the slashes", f), example)
	}
	return f
}

// cleanStaticDir normalizes the folder: forward slashes, no trailing slash,
// relative to deploy.yaml and inside its directory. Only the CLI reads the
// folder, but a path that climbs out of the project is a mistake wherever it
// is read.
func cleanStaticDir(s string) (dir, problem string) {
	s = strings.TrimSpace(strings.ReplaceAll(s, `\`, "/"))
	switch {
	case s == "":
		return "", "must not be empty"
	case strings.ContainsAny(s, "\x00\r\n"):
		return "", "must not contain control characters"
	case strings.HasPrefix(s, "/"), len(s) > 1 && s[1] == ':':
		return "", "must be a relative path: the folder is found next to deploy.yaml"
	}
	dir = path.Clean(s)
	if dir == ".." || strings.HasPrefix(dir, "../") {
		return "", "must stay inside the directory of deploy.yaml"
	}
	return dir, ""
}

// MaxStaticBytes bounds the folder of a static application, as the tar archive
// the CLI sends and the agent keeps.
const MaxStaticBytes = 512 << 20

// staticRaw accepts `static: dist/` and `static: {dir: dist/, fallback: index.html}`.
type staticRaw struct {
	Dir      string `yaml:"dir"`
	Fallback string `yaml:"fallback"`
}

func (s *staticRaw) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		return value.Decode(&s.Dir)
	}
	// A node is decoded without the strictness of the document's decoder; a
	// misspelt key is reported here, in that decoder's words.
	if unknown := unknownKeys(value, reflect.TypeOf(s)); len(unknown) > 0 {
		return &yaml.TypeError{Errors: unknown}
	}
	type plain staticRaw
	return value.Decode((*plain)(s))
}
