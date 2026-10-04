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
// from --env-file. Only the ${NAME} form is recognized (spec.Placeholder) — a
// bare $NAME is left alone, and $${NAME} yields a literal ${NAME}.
//
// Env values, and the passwords of proxy.basic_auth with them, are the
// exception in two ways, because the agent fills them in too, from the
// secrets stored on the server (`shipwick secret set`): a name
// that is set nowhere here is left in place for the server rather than being
// an error, and $${NAME} is left as it is, for the agent to turn into ${NAME}
// — done here, the agent would take the result for a placeholder. Anywhere
// else — an image tag, a command — a name that is set nowhere is an error,
// never silently empty: an empty password is the worse surprise.
//
// Placeholders are looked for in values only, by walking the document: a
// comment that explains ${NAME}, or a key, is not a reference.

// placeholders is what expand did with a document's ${NAME} references. It
// carries names only, never values.
type placeholders struct {
	substituted []string // filled in here, from the environment or --env-file
	deferred    []string // secret values left for the agent to fill in from its secrets
	// plain is, for each application of the file in its order, the env
	// variables whose value stands in the file as it is sent: nothing in it
	// was filled in here. It is what the agent is told may be shown again
	// (plainOf); a value with anything filled in is never in it.
	plain [][]string
}

// plainOf is the statement sent with the deployment of the i-th application
// of the file: the fields of app whose value was written in the file in plain
// sight. A file the walk and the parser read differently says nothing.
func (p placeholders) plainOf(i int, app spec.App) []string {
	if i >= len(p.plain) {
		return nil
	}
	var fields []string
	for _, name := range p.plain[i] {
		if _, ok := app.Env[name]; ok {
			fields = append(fields, "env."+name)
		}
	}
	return fields
}

// note is the parenthesis after "Validated deploy.yaml".
func (p placeholders) note() string {
	var parts []string
	if n := len(p.substituted); n > 0 {
		parts = append(parts, plural(n, "variable")+" substituted")
	}
	if n := len(p.deferred); n > 0 {
		if len(parts) == 0 {
			parts = append(parts, plural(n, "variable")+" left to the server")
		} else {
			parts = append(parts, fmt.Sprintf("%d left to the server", n))
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return " (" + strings.Join(parts, ", ") + ")"
}

// expand substitutes placeholders in data. A document that does not parse is
// returned as it is, for spec.Parse to explain.
func expand(data []byte, lookup func(string) (string, bool)) ([]byte, placeholders, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil || doc.Kind != yaml.DocumentNode {
		return data, placeholders{}, nil
	}
	x := &expander{lookup: lookup, seen: map[string]bool{}}
	for _, root := range doc.Content {
		x.walkRoot(root)
	}
	if len(x.missing) > 0 {
		sort.Strings(x.missing)
		missing := unique(x.missing)
		return nil, placeholders{}, fmt.Errorf("refers to %s, which %s not set\n\nSet %s in the environment, or in a file given with --env-file. Only values under env and the passwords of proxy.basic_auth can be left to the secrets on the server.",
			quoteAll(missing), pluralIs(len(missing)), pluralIt(len(missing)))
	}
	if !x.changed {
		return data, x.placeholders, nil
	}
	out, err := yaml.Marshal(&doc)
	if err != nil {
		return nil, placeholders{}, err
	}
	return out, x.placeholders, nil
}

type expander struct {
	lookup  func(string) (string, bool)
	seen    map[string]bool
	missing []string
	changed bool
	filled  int // how many placeholders were replaced by a value so far
	placeholders
}

// walkRoot walks the top-level mapping, where the env block and the proxy
// block are told apart: env values and basic-auth passwords are the ones the
// agent fills in from its secrets. In a shipwick.yaml every entry of apps is
// such a mapping.
func (x *expander) walkRoot(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		x.walk(n, false)
		return
	}
	many := false
	var plain []string
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i].Value, n.Content[i+1]
		if key == "apps" && value.Kind == yaml.SequenceNode {
			many = true
			for _, entry := range value.Content {
				x.walkRoot(entry)
			}
			continue
		}
		if key == "proxy" {
			x.walkProxy(value)
			continue
		}
		if key == "env" && value.Kind == yaml.MappingNode {
			plain = x.walkEnv(value)
			continue
		}
		x.walk(value, key == "env")
	}
	if !many {
		x.plain = append(x.plain, plain)
	}
}

// walkEnv walks the env block and returns the variables whose value is sent
// as the file wrote it. Only a scalar counts: an alias repeats a value that
// may have been filled in where it was defined.
func (x *expander) walkEnv(n *yaml.Node) (plain []string) {
	for i := 0; i+1 < len(n.Content); i += 2 {
		value := n.Content[i+1]
		before := x.filled
		x.walk(value, true)
		if value.Kind == yaml.ScalarNode && x.filled == before {
			plain = append(plain, n.Content[i].Value)
		}
	}
	return plain
}

// walkProxy walks the proxy block, whose one secret is the password of a
// basic_auth entry.
func (x *expander) walkProxy(n *yaml.Node) {
	if n.Kind != yaml.MappingNode {
		x.walk(n, false)
		return
	}
	for i := 0; i+1 < len(n.Content); i += 2 {
		key, value := n.Content[i].Value, n.Content[i+1]
		if key != "basic_auth" || value.Kind != yaml.SequenceNode {
			x.walk(value, false)
			continue
		}
		for _, entry := range value.Content {
			if entry.Kind != yaml.MappingNode {
				x.walk(entry, false)
				continue
			}
			for j := 0; j+1 < len(entry.Content); j += 2 {
				x.walk(entry.Content[j+1], entry.Content[j].Value == "password")
			}
		}
	}
}

func (x *expander) walk(n *yaml.Node, env bool) {
	switch n.Kind {
	case yaml.SequenceNode:
		for _, c := range n.Content {
			x.walk(c, env)
		}
	case yaml.MappingNode:
		for i := 0; i+1 < len(n.Content); i += 2 {
			x.walk(n.Content[i+1], env)
		}
	case yaml.ScalarNode:
		if !spec.Placeholder.MatchString(n.Value) {
			return
		}
		n.Value = spec.Placeholder.ReplaceAllStringFunc(n.Value, func(match string) string {
			m := spec.Placeholder.FindStringSubmatch(match)
			if m[1] != "" {
				if env {
					return match
				}
				x.changed = true
				return "${" + m[2] + "}"
			}
			value, ok := x.lookup(m[2])
			if !ok {
				if env {
					x.remember(&x.deferred, m[2])
				} else {
					x.missing = append(x.missing, m[2])
				}
				return match
			}
			x.remember(&x.substituted, m[2])
			x.changed = true
			x.filled++
			return value
		})
		// A number or a boolean written through a placeholder must stay
		// the string the user wrote; the tag decides how YAML reads it back.
		n.Tag = "!!str"
		n.Style = 0
	}
}

// remember records a name once, in order of first appearance.
func (x *expander) remember(names *[]string, name string) {
	if x.seen[name] {
		return
	}
	x.seen[name] = true
	*names = append(*names, name)
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
// it. data is what is sent to the agent: complete but for the secret values
// the server fills in from what it has stored.
func (c *cli) loadConfig(path string, envFiles []string, image string) (data []byte, app spec.App, vars placeholders, err error) {
	data, err = readFile(path)
	if err != nil {
		return nil, spec.App{}, placeholders{}, err
	}
	files, err := loadEnvFiles(envFiles)
	if err != nil {
		return nil, spec.App{}, placeholders{}, err
	}
	data, vars, err = expand(data, c.lookup(files))
	if err != nil {
		return nil, spec.App{}, placeholders{}, fmt.Errorf("%s: %w", path, err)
	}
	if image != "" {
		data = overrideImage(data, image)
	}
	app, err = spec.Parse(data)
	return data, app, vars, err
}

// printDeferred says, under a validation summary, which values the server
// will fill in. It cannot say whether the server has them: that is
// the deployment's first check.
func (c *cli) printDeferred(vars placeholders) {
	if len(vars.deferred) == 0 {
		return
	}
	c.ui.Println()
	for _, name := range vars.deferred {
		c.ui.Println("  ${" + name + "} is not set here; the server fills it in from its secrets")
	}
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
