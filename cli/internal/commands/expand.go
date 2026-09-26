package commands

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"sort"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/shipwick/shipwick/pkg/spec"
)

// A deploy.yaml may refer to values it must not contain: ${DATABASE_PASSWORD}
// is replaced before the file is validated or sent, from the environment and
// from --env-file. Only the ${NAME} form is recognized — a bare $NAME is left
// alone, and $${NAME} yields a literal ${NAME}. A name that is set nowhere is
// an error, never silently empty: an empty password is the worse surprise.
//
// Placeholders are looked for in values only, by walking the document: a
// comment that explains ${NAME}, or a key, is not a reference.
var placeholder = regexp.MustCompile(`\$(\$?)\{([A-Za-z_][A-Za-z0-9_]*)\}`)

// expand substitutes placeholders in data. It returns the names substituted,
// for the summary line, and never the values. A document that does not parse
// is returned as it is, for spec.Parse to explain.
func expand(data []byte, lookup func(string) (string, bool)) ([]byte, []string, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Kind != yaml.DocumentNode {
		return data, nil, nil
	}
	x := &expander{lookup: lookup, seen: map[string]bool{}}
	x.walk(&doc, false)
	if len(x.missing) > 0 {
		sort.Strings(x.missing)
		missing := unique(x.missing)
		return nil, nil, fmt.Errorf("refers to %s, which %s not set\n\nSet %s in the environment, or in a file given with --env-file.",
			quoteAll(missing), pluralIs(len(missing)), pluralIt(len(missing)))
	}
	if !x.changed {
		return data, nil, nil
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, nil, err
	}
	return out, x.used, nil
}

type expander struct {
	lookup  func(string) (string, bool)
	seen    map[string]bool
	used    []string
	missing []string
	changed bool
}

func (x *expander) walk(n *yaml.Node, isKey bool) {
	switch n.Kind {
	case yaml.DocumentNode, yaml.SequenceNode:
		for _, c := range n.Content {
			x.walk(c, false)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			x.walk(n.Content[i+1], false)
		}
	case yaml.ScalarNode:
		if isKey || !placeholder.MatchString(n.Value) {
			return
		}
		n.Value = placeholder.ReplaceAllStringFunc(n.Value, func(match string) string {
			m := placeholder.FindStringSubmatch(match)
			if m[1] != "" {
				x.changed = true
				return "${" + m[2] + "}"
			}
			value, ok := x.lookup(m[2])
			if !ok {
				x.missing = append(x.missing, m[2])
				return match
			}
			if !x.seen[m[2]] {
				x.seen[m[2]] = true
				x.used = append(x.used, m[2])
			}
			x.changed = true
			return value
		})
		// A number or a boolean written through a placeholder must stay
		// the string the user wrote; the tag decides how YAML reads it back.
		n.Tag = "!!str"
		n.Style = 0
	}
}

// loadEnvFiles reads KEY=VALUE files, later files overriding earlier ones.
// Lines starting with # and blank lines are ignored; a leading "export " and
// surrounding single or double quotes on the value are stripped.
func loadEnvFiles(paths []string) (map[string]string, error) {
	values := map[string]string{}
	for _, path := range paths {
		f, err := os.Open(path)
		if err != nil {
			return nil, err
		}
		scanner := bufio.NewScanner(f)
		for n := 1; scanner.Scan(); n++ {
			line := strings.TrimSpace(scanner.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			line = strings.TrimPrefix(line, "export ")
			key, value, ok := strings.Cut(line, "=")
			key = strings.TrimSpace(key)
			if !ok || !envName.MatchString(key) {
				f.Close()
				return nil, fmt.Errorf("%s:%d: expected NAME=value", path, n)
			}
			value = strings.TrimSpace(value)
			if len(value) >= 2 && (value[0] == '"' || value[0] == '\'') && value[len(value)-1] == value[0] {
				value = value[1 : len(value)-1]
			}
			values[key] = value
		}
		f.Close()
		if err := scanner.Err(); err != nil {
			return nil, fmt.Errorf("%s: %w", path, err)
		}
	}
	return values, nil
}

var envName = regexp.MustCompile(`^[A-Za-z_][A-Za-z0-9_]*$`)

// lookup resolves a placeholder: the process environment wins over env files,
// so that a CI secret can override what a checked-in file says.
func (c *cli) lookup(files map[string]string) func(string) (string, bool) {
	return func(name string) (string, bool) {
		if v := c.getenv(name); v != "" {
			return v, true
		}
		v, ok := files[name]
		return v, ok
	}
}

// loadConfig reads a deploy.yaml, substitutes its placeholders and validates
// it. data is what is sent to the agent: complete, with nothing left to
// resolve on the server.
func (c *cli) loadConfig(path string, envFiles []string, image string) (data []byte, app spec.App, substituted []string, err error) {
	data, err = readFile(path)
	if err != nil {
		return nil, spec.App{}, nil, err
	}
	files, err := loadEnvFiles(envFiles)
	if err != nil {
		return nil, spec.App{}, nil, err
	}
	data, substituted, err = expand(data, c.lookup(files))
	if err != nil {
		return nil, spec.App{}, nil, fmt.Errorf("%s: %w", path, err)
	}
	if image != "" {
		data = overrideImage(data, image)
	}
	app, err = spec.Parse(data)
	return data, app, substituted, err
}

func unique(names []string) []string {
	var out []string
	for i, n := range names {
		if i == 0 || n != names[i-1] {
			out = append(out, n)
		}
	}
	return out
}

func quoteAll(names []string) string {
	q := make([]string, len(names))
	for i, n := range names {
		q[i] = "${" + n + "}"
	}
	return strings.Join(q, ", ")
}

func pluralIs(n int) string {
	if n == 1 {
		return "is"
	}
	return "are"
}

func pluralIt(n int) string {
	if n == 1 {
		return "it"
	}
	return "them"
}
