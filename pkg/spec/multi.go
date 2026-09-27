package spec

import (
	"errors"
	"fmt"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MultiFile is the file that describes several applications at once.
const MultiFile = "shipwick.yaml"

// MaxApps bounds the applications of one shipwick.yaml.
const MaxApps = 50

// Entry is one application of a shipwick.yaml.
type Entry struct {
	App App
	// After names the applications of the same file that must have deployed
	// before this one starts.
	After []string
	// Config is the entry as a deploy.yaml document, exactly what the agent
	// receives: the agent knows nothing of shipwick.yaml, only of applications.
	Config []byte
}

// IsMany reports whether data is a shipwick.yaml — a document whose top-level
// key is `apps` — rather than a deploy.yaml. Anything that does not parse is
// a deploy.yaml, for Parse to explain.
func IsMany(data []byte) bool {
	var doc struct {
		Apps yaml.Node `yaml:"apps"`
	}
	return yaml.Unmarshal(data, &doc) == nil && !doc.Apps.IsZero()
}

// ParseMany decodes and validates a shipwick.yaml: an `apps` list in which
// every entry is a complete deploy.yaml document plus `after`, the names of
// the applications in the same file it must wait for. Each entry goes through
// Parse, so every rule of deploy.yaml applies unchanged; field names in the
// report are prefixed with the entry, `apps[1].port`. On invalid input the
// error is a *ValidationError listing every problem found.
func ParseMany(data []byte) ([]Entry, error) {
	verr := &ValidationError{File: MultiFile}

	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		verr.add(MultiFile, strings.TrimPrefix(err.Error(), "yaml: "), "")
		return nil, verr
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		verr.add(MultiFile, "expected a mapping with an apps list", "apps:\n    - name: api\n      image: ...")
		return nil, verr
	}

	// Every other top-level key is a deploy.yaml key that lost its way: the
	// two kinds of file are not mixed, or "which name is the name" has no
	// answer.
	var apps *yaml.Node
	root := doc.Content[0]
	for i := 0; i+1 < len(root.Content); i += 2 {
		key := root.Content[i].Value
		if key == "apps" {
			apps = root.Content[i+1]
			continue
		}
		verr.add(key, fmt.Sprintf("cannot be set at the top of %s; move it into an entry under apps", MultiFile), "")
	}
	switch {
	case apps == nil:
		verr.add("apps", "is required", "one entry per application, each with name and image")
	case apps.Kind != yaml.SequenceNode || len(apps.Content) == 0:
		verr.add("apps", "must be a list of applications", "one entry per application, each with name and image")
	case len(apps.Content) > MaxApps:
		verr.add("apps", fmt.Sprintf("lists %d applications (max %d)", len(apps.Content), MaxApps), "")
	}
	if len(verr.Fields) > 0 {
		return nil, verr
	}

	entries := make([]Entry, len(apps.Content))
	// names holds what each entry calls itself even when the rest of it does
	// not validate, so that `after` is checked against every entry and the
	// user sees all mistakes at once.
	names := make([]string, len(apps.Content))
	for i, node := range apps.Content {
		names[i], entries[i] = parseEntry(i, node, verr)
	}
	validateAfter(names, entries, verr)

	if len(verr.Fields) > 0 {
		return nil, verr
	}
	return entries, nil
}

// parseEntry strips `after` from one entry, re-encodes the rest as a
// deploy.yaml document and hands that to Parse.
func parseEntry(i int, node *yaml.Node, verr *ValidationError) (name string, e Entry) {
	field := fmt.Sprintf("apps[%d]", i)
	if node.Kind != yaml.MappingNode {
		verr.add(field, "expected an application: a mapping with name, image, ...", "")
		return "", e
	}

	rest := &yaml.Node{Kind: yaml.MappingNode, Tag: node.Tag, Style: node.Style}
	for j := 0; j+1 < len(node.Content); j += 2 {
		key, value := node.Content[j], node.Content[j+1]
		switch key.Value {
		case "name":
			name = value.Value
		case "after":
			e.After = afterNames(field+".after", value, verr)
			continue
		}
		rest.Content = append(rest.Content, key, value)
	}

	config, err := yaml.Marshal(rest)
	if err != nil {
		verr.add(field, err.Error(), "")
		return name, e
	}
	e.Config = config
	e.App, err = Parse(config)
	var inner *ValidationError
	if errors.As(err, &inner) {
		for _, f := range inner.Fields {
			// Whole-document complaints (an unknown key, reported by line)
			// carry line numbers of the re-encoded entry, which the user has
			// never seen; the entry itself is the useful address.
			if f.Field == "deploy.yaml" || strings.HasPrefix(f.Field, "line ") {
				f.Field = field
			} else {
				f.Field = field + "." + f.Field
			}
			verr.Fields = append(verr.Fields, f)
		}
	}
	return name, e
}

func afterNames(field string, node *yaml.Node, verr *ValidationError) []string {
	if node.Kind != yaml.SequenceNode {
		verr.add(field, "must be a list of application names from this file", "after: [postgres]")
		return nil
	}
	names := make([]string, 0, len(node.Content))
	for _, n := range node.Content {
		if n.Kind != yaml.ScalarNode || n.Value == "" {
			verr.add(field, "must be a list of application names from this file", "after: [postgres]")
			return nil
		}
		names = append(names, n.Value)
	}
	return names
}

// validateAfter checks the dependency graph: names are unique, every `after`
// names another entry of the file, and nothing waits for itself, directly or
// through others.
func validateAfter(names []string, entries []Entry, verr *ValidationError) {
	index := map[string]int{}
	for i, name := range names {
		if name == "" {
			continue // reported by Parse as missing
		}
		if first, dup := index[name]; dup {
			verr.add(fmt.Sprintf("apps[%d].name", i), fmt.Sprintf("%q is also the name of apps[%d]; names must be unique within the file", name, first), "")
			continue
		}
		index[name] = i
	}

	complete := true
	for i, e := range entries {
		for _, dep := range e.After {
			field := fmt.Sprintf("apps[%d].after", i)
			switch j, known := index[dep]; {
			case dep == names[i]:
				verr.add(field, fmt.Sprintf("%s cannot wait for itself", dep), "")
				complete = false
			case !known:
				verr.add(field, fmt.Sprintf("%q is not an application in this file", dep), "")
				complete = false
			case j == i:
				// A duplicate name shadowed by an earlier entry; already reported.
				complete = false
			}
		}
	}
	if !complete || len(index) != len(names) {
		return
	}

	// Depth-first search over the graph; a back edge is a cycle, reported once,
	// by the names on it, at the entry where it was found.
	const (
		unseen = iota
		visiting
		finished
	)
	state := make([]int, len(entries))
	var path []string
	var visit func(i int) bool
	visit = func(i int) bool {
		state[i] = visiting
		path = append(path, names[i])
		for _, dep := range entries[i].After {
			j := index[dep]
			switch state[j] {
			case visiting:
				cycle := path[indexOf(path, dep):]
				verr.add(fmt.Sprintf("apps[%d].after", i),
					fmt.Sprintf("%s: an application cannot wait for itself", strings.Join(append(cycle, dep), " → ")), "")
				return true
			case unseen:
				if visit(j) {
					return true
				}
			}
		}
		path = path[:len(path)-1]
		state[i] = finished
		return false
	}
	for i := range entries {
		if state[i] == unseen && visit(i) {
			return
		}
	}
}

func indexOf(list []string, s string) int {
	for i, v := range list {
		if v == s {
			return i
		}
	}
	return -1
}
