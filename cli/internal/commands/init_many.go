package commands

import (
	"errors"
	"fmt"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"slices"
	"strings"

	"go.yaml.in/yaml/v3"

	"github.com/shipwick/shipwick/pkg/spec"
)

// In a directory with a shipwick.yaml, `shipwick init` adds an application to
// that file instead of writing a deploy.yaml next to it. The file is the
// user's — its comments, its order, its spacing — so the entry is appended as
// text at the end of the `apps` list and nothing else is touched. Where that
// cannot be done with certainty, the file is left alone and the user is shown
// what to add.

// entryDir is the directory whose project becomes the new entry: the current
// one, or the one given, which must be a folder of this project since `build`
// and `static` paths may not leave it.
func entryDir(args []string, named bool) (string, error) {
	if len(args) == 0 {
		return ".", nil
	}
	if named {
		return "", errors.New("a directory is where init looks for a project; --image and --static say what to deploy themselves, so leave one of them out")
	}
	dir := filepath.Clean(args[0])
	if !filepath.IsLocal(dir) {
		return "", fmt.Errorf("%s is outside this directory, and an entry of %s can only build what is under it", args[0], spec.MultiFile)
	}
	info, err := os.Stat(dir)
	if err != nil || !info.IsDir() {
		return "", fmt.Errorf("%s is not a directory here", args[0])
	}
	return filepath.ToSlash(dir), nil
}

// initEntry adds the answers as an entry of shipwick.yaml, and writes the
// Dockerfile and .dockerignore of a recognised project into dir.
func (c *cli) initEntry(a initAnswers, dir string) error {
	file := spec.MultiFile
	data, err := os.ReadFile(file)
	if err != nil {
		return err
	}
	entry := renderEntry(a, dir)
	if _, err := spec.Parse([]byte(strings.Join(entry, "\n") + "\n")); err != nil {
		return err
	}
	names, err := entryNames(data)
	if err != nil {
		return fmt.Errorf("%s cannot be read, so nothing was added to it: %w", file, err)
	}
	if slices.Contains(names, a.Name) {
		return fmt.Errorf("%s already has an application named %s\n\nEdit that entry, or add this one under another name: shipwick init --name <name>", file, a.Name)
	}
	updated, unsafe := appendEntry(data, a.Name, entry)

	written, kept, err := writeProjectFiles(dir, a.Project)
	if err != nil {
		return err
	}
	in := func(names []string) []string {
		shown := make([]string, len(names))
		for i, name := range names {
			shown[i] = path.Join(dir, name)
		}
		return shown
	}
	if p := a.Project; p != nil {
		c.ui.Success("Recognised %s", p.Label)
		if len(kept) > 0 {
			c.ui.Success("Kept the existing %s; build: %s will use %s", joinFiles(in(kept)), buildPath(dir), itOrThem(len(kept)))
		}
		if len(written) > 0 {
			c.ui.Success("Wrote %s", joinFiles(in(written)))
		}
		c.noteNoLock(p, written)
	}

	if unsafe != nil {
		c.ui.Failure("Left %s as it is: %s", file, unsafe)
		c.ui.Println()
		c.ui.Println("Add this to its apps list by hand:")
		c.ui.Println()
		for i, line := range entry {
			lead := "    "
			if i == 0 {
				lead = "  - "
			}
			c.ui.Println(lead + line)
		}
		return ErrReported
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(file); err == nil {
		mode = info.Mode().Perm()
	}
	if err := os.WriteFile(file, updated, mode); err != nil {
		return err
	}
	c.ui.Success("Added %s to %s", a.Name, file)
	c.ui.Println()
	c.ui.Println(c.initClosing(len(written) + 1))
	return nil
}

// buildPath is dir as `build:` names it.
func buildPath(dir string) string {
	if dir == "." {
		return "."
	}
	return "./" + dir
}

// renderEntry writes the answers as the lines of one entry, without the dash
// and the indentation of the list they go into. Unlike the starter
// deploy.yaml it carries no commented-out examples: the file it joins has its
// own voice, and deploy.example.yaml lists every key.
func renderEntry(a initAnswers, dir string) []string {
	lines := []string{"name: " + a.Name}
	add := func(format string, args ...any) { lines = append(lines, fmt.Sprintf(format, args...)) }

	p := a.Project
	switch {
	case p != nil && p.Kind == kindStatic:
		folder := path.Join(dir, p.Static.Dir)
		if folder != "." {
			folder += "/"
		}
		if p.Static.Build != "" {
			where := ""
			if dir != "." {
				where = " in " + dir + "/"
			}
			add("# What `%s` writes; run it%s before `shipwick deploy`.", p.Static.Build, where)
		}
		add("static: %s", folder)
	case p != nil:
		add("build: %s", buildPath(dir))
	default:
		add("image: %s", a.Image)
	}
	if a.Port != 0 {
		add("port: %d", a.Port)
	}
	if a.Domain != "" {
		add("domain: %s", a.Domain)
	}
	if p != nil && p.HealthLive && a.Port != 0 {
		add("health:")
		add("  path: %s", p.HealthPath)
	}
	return lines
}

// entryNames lists the names of the applications in a shipwick.yaml, without
// validating them: a file with a mistake elsewhere can still be added to.
func entryNames(data []byte) ([]string, error) {
	var doc struct {
		Apps []struct {
			Name string `yaml:"name"`
		} `yaml:"apps"`
	}
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, errors.New(strings.TrimPrefix(err.Error(), "yaml: "))
	}
	names := make([]string, len(doc.Apps))
	for i, app := range doc.Apps {
		names[i] = app.Name
	}
	return names, nil
}

// dashPrefix is what precedes the first key of a list entry written the usual
// way: indentation, the dash, the gap.
var dashPrefix = regexp.MustCompile(`^( *)-( +)$`)

// appendEntry returns data with one more entry at the end of its `apps` list,
// indented as the entry before it. Nothing else changes, not even the line
// endings. The error says why the file cannot be edited with certainty: the
// list is not the last thing in the file, or is not written as a plain block
// list, or the result did not read back as the same file plus one entry.
func appendEntry(data []byte, name string, entry []string) ([]byte, error) {
	var doc yaml.Node
	if err := yaml.Unmarshal(data, &doc); err != nil {
		return nil, errors.New(strings.TrimPrefix(err.Error(), "yaml: "))
	}
	if doc.Kind != yaml.DocumentNode || len(doc.Content) != 1 || doc.Content[0].Kind != yaml.MappingNode {
		return nil, errors.New("it is not a mapping with an apps list")
	}
	root := doc.Content[0]
	if n := len(root.Content); n < 2 || root.Content[n-2].Value != "apps" {
		return nil, errors.New("apps is not its last key, so the end of the file is not the end of the list")
	}
	apps := root.Content[len(root.Content)-1]

	eol := "\n"
	if strings.Contains(string(data), "\r\n") {
		eol = "\r\n"
	}
	lines := strings.Split(strings.ReplaceAll(string(data), "\r\n", "\n"), "\n")
	for _, line := range lines[1:] {
		if strings.HasPrefix(line, "---") || line == "..." {
			return nil, errors.New("it holds more than one document")
		}
	}

	indent, gap, spaced := "  ", " ", false
	switch {
	case apps.Kind == yaml.ScalarNode && apps.Tag == "!!null" && apps.Value == "":
		// `apps:` with nothing under it yet.
	case apps.Kind != yaml.SequenceNode || apps.Style&yaml.FlowStyle != 0 || len(apps.Content) == 0:
		return nil, errors.New("apps is not written as a list with one entry per dash")
	default:
		last := apps.Content[len(apps.Content)-1]
		if last.Kind != yaml.MappingNode || last.Style&yaml.FlowStyle != 0 || last.Line < 1 || last.Line > len(lines) || last.Column < 1 || last.Column-1 > len(lines[last.Line-1]) {
			return nil, errors.New("the last entry of apps is not written as a block of keys")
		}
		m := dashPrefix.FindStringSubmatch(lines[last.Line-1][:last.Column-1])
		if m == nil {
			return nil, errors.New("the last entry of apps does not start on the line of its dash")
		}
		indent, gap = m[1], m[2]
		// Entries separated by an empty line get one before the new entry too.
		above := last.Line - 2
		for above >= 0 && strings.HasPrefix(strings.TrimSpace(lines[above]), "#") {
			above--
		}
		spaced = len(apps.Content) > 1 && above >= 0 && strings.TrimSpace(lines[above]) == ""
	}

	var b strings.Builder
	b.WriteString(strings.TrimRight(string(data), " \t\r\n"))
	b.WriteString(eol)
	if spaced {
		b.WriteString(eol)
	}
	for i, line := range entry {
		lead := indent + " " + gap
		if i == 0 {
			lead = indent + "-" + gap
		}
		b.WriteString(lead + line + eol)
	}
	updated := []byte(b.String())

	// The proof that the text went where it was meant to: the same
	// applications as before, and the new one after them.
	before, _ := entryNames(data)
	after, err := entryNames(updated)
	if err != nil || !slices.Equal(after, append(slices.Clone(before), name)) {
		return nil, errors.New("the entry could not be placed at the end of apps with certainty")
	}
	return updated, nil
}
