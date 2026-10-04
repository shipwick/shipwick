package spec

import (
	"bytes"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v3"
)

// Security takes away from an application's containers what every container
// has by default and this application does not need. Every field narrows;
// none of them can widen what a container is given: there is no way to add a
// capability, to ask for privileges or to mount anything from the server.
type Security struct {
	// ReadOnly makes the root filesystem read-only. Volumes stay writable, and
	// so does every path of Tmpfs.
	ReadOnly bool `json:"read_only,omitempty"`
	// Tmpfs are directories kept in memory: scratch space that is empty when
	// a container starts and gone when it stops.
	Tmpfs []Tmpfs `json:"tmpfs,omitempty"`
	// Capabilities are the Linux capabilities the containers keep, out of
	// Docker's default set. Nil keeps that whole set; a pointer to an empty
	// list keeps none.
	Capabilities *[]string `json:"capabilities,omitempty"`
	// NonRoot refuses to start a container that would run as root.
	NonRoot bool `json:"non_root,omitempty"`
}

// Tmpfs is one directory kept in memory.
type Tmpfs struct {
	Path      string `json:"path"` // absolute path inside the container
	SizeBytes int64  `json:"size_bytes"`
}

// Bounds of security.tmpfs. What is written there is memory, counted against
// resources.memory like the rest of what the container uses.
const (
	MaxTmpfs          = 10
	DefaultTmpfsBytes = 64 << 20
	MinTmpfsBytes     = 1 << 20
	MaxTmpfsBytes     = 1 << 30
)

// DefaultCapabilities is the set Docker gives a container that asks for
// nothing, and the only names security.capabilities accepts: a capability
// outside it would be one the container does not have today.
var DefaultCapabilities = []string{
	"AUDIT_WRITE", "CHOWN", "DAC_OVERRIDE", "FOWNER", "FSETID", "KILL", "MKNOD",
	"NET_BIND_SERVICE", "NET_RAW", "SETFCAP", "SETGID", "SETPCAP", "SETUID", "SYS_CHROOT",
}

// CapabilitiesNone is what security.capabilities says to keep none.
const CapabilitiesNone = "none"

// securityRaw mirrors the `security` block.
type securityRaw struct {
	ReadOnly     *bool           `yaml:"read_only"`
	Tmpfs        []tmpfsRaw      `yaml:"tmpfs"`
	Capabilities capabilitiesRaw `yaml:"capabilities"`
	NonRoot      *bool           `yaml:"non_root"`
}

// tmpfsRaw accepts `- /tmp` and `- {path: /tmp, size: 128mb}`.
type tmpfsRaw struct {
	Path string `yaml:"path"`
	Size string `yaml:"size"`
}

func (t *tmpfsRaw) UnmarshalYAML(value *yaml.Node) error {
	if value.Kind == yaml.ScalarNode {
		return value.Decode(&t.Path)
	}
	// As for static: a node is decoded without the document decoder's
	// strictness, and a misspelt `size` must not pass for the default.
	if unknown := unknownKeys(value, reflect.TypeOf(t)); len(unknown) > 0 {
		return &yaml.TypeError{Errors: unknown}
	}
	type plain tmpfsRaw
	return value.Decode((*plain)(t))
}

// capabilitiesRaw accepts the word `none` and a list of names. given tells a
// key that was written from one that was not: the two mean opposite things.
type capabilitiesRaw struct {
	given bool
	word  string
	names []string
}

func (c *capabilitiesRaw) UnmarshalYAML(value *yaml.Node) error {
	c.given = true
	switch value.Kind {
	case yaml.ScalarNode:
		c.word = value.Value
		return nil
	case yaml.SequenceNode:
		// Never nil: an empty list was still written.
		c.names = []string{}
		return value.Decode(&c.names)
	}
	return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: expected none or a list of capabilities", value.Line)}}
}

const securityKeys = "read_only, tmpfs, capabilities, non_root"

// validateSecurity checks the `security` block. A block that asks for nothing
// is no block: the App says nil for it, as it does when the key is absent.
func (r raw) validateSecurity(verr *ValidationError, app App) *Security {
	if r.Security.IsZero() {
		return nil
	}
	const example = "security:\n    read_only: true\n    tmpfs: [/tmp]"
	if r.Security.Kind != yaml.MappingNode {
		verr.add("security", "must be a block", example)
		return nil
	}
	// Decoded from its own text, as backups is: a node's Decode accepts keys
	// it does not know, and a typo in `read_only` must not pass for a root
	// filesystem that may be written.
	text, err := yaml.Marshal(&r.Security)
	if err != nil {
		verr.add("security", "could not be read", example)
		return nil
	}
	var s securityRaw
	dec := yaml.NewDecoder(bytes.NewReader(text))
	dec.KnownFields(true)
	if err := dec.Decode(&s); err != nil {
		problems, _ := syntaxError(err)
		for _, f := range problems.Fields {
			verr.add("security", f.Message, securityKeys)
		}
		return nil
	}
	if r.Static.Dir != "" {
		verr.add("security", staticExclusiveMessage, "")
		return nil
	}

	out := &Security{
		ReadOnly: s.ReadOnly != nil && *s.ReadOnly,
		NonRoot:  s.NonRoot != nil && *s.NonRoot,
	}
	out.Tmpfs = validateTmpfs(verr, s.Tmpfs, app.Volumes)
	out.Capabilities = validateCapabilities(verr, s.Capabilities)
	if out.NonRoot {
		// What deploy.yaml itself says is known now; what the image says is
		// known to the agent once it has the image.
		if problem := RootUser(app.User); app.User != "" && problem != "" {
			verr.add("user", fmt.Sprintf("invalid value %q next to security.non_root: %s", app.User, problem),
				"a numeric id other than 0, with or without a group: 1000, 1000:1000")
		}
	}
	if !out.ReadOnly && !out.NonRoot && len(out.Tmpfs) == 0 && out.Capabilities == nil {
		return nil
	}
	return out
}

// RootUser says why a container started as user — the form of deploy.yaml's
// `user` and of an image's USER — cannot be held to run as someone other than
// root, and returns "" when it can: when the user is a numeric id other than
// 0. A name is not enough. Which id it stands for is written in the image's
// /etc/passwd, which nothing here reads, and an image is free to give the id
// 0 a second name.
func RootUser(user string) string {
	name, _, _ := strings.Cut(strings.TrimSpace(user), ":")
	if name == "" {
		return "no user is named, and a container without one runs as root"
	}
	id, err := strconv.ParseUint(name, 10, 64)
	switch {
	case err != nil && name == "root":
		return "it is root"
	case err != nil:
		return "it is a name, and which id a name stands for is in the image's /etc/passwd, which is not read"
	case id == 0:
		return "id 0 is root"
	}
	return ""
}

func validateTmpfs(verr *ValidationError, entries []tmpfsRaw, volumes []Volume) []Tmpfs {
	if len(entries) == 0 {
		return nil
	}
	if len(entries) > MaxTmpfs {
		verr.add("security.tmpfs", fmt.Sprintf("too many (%d)", len(entries)), fmt.Sprintf("at most %d", MaxTmpfs))
		return nil
	}
	mounted := map[string]string{}
	for _, v := range volumes {
		mounted[v.Path] = "volume " + v.Name
	}
	out := make([]Tmpfs, 0, len(entries))
	for i, entry := range entries {
		field := fmt.Sprintf("security.tmpfs[%d]", i)
		p := strings.TrimSpace(entry.Path)
		switch {
		case p == "":
			verr.add(field+".path", "is required", "/tmp")
		case !validMountPath(p):
			verr.add(field+".path", fmt.Sprintf("invalid value %q", entry.Path), "an absolute path inside the container, e.g. /tmp")
		case mounted[p] != "":
			verr.add(field+".path", fmt.Sprintf("%q is already mounted: %s", p, mounted[p]), "a path nothing else is mounted at")
		}
		mounted[p] = "security.tmpfs"

		size := int64(DefaultTmpfsBytes)
		if entry.Size != "" {
			n, err := parseSize(entry.Size)
			switch {
			case err != nil:
				verr.add(field+".size", err.Error(), "16mb, 64mb, 256mb, ... (1mb to 1gb)")
			case n < MinTmpfsBytes || n > MaxTmpfsBytes:
				verr.add(field+".size", fmt.Sprintf("invalid value %q: out of range", entry.Size), "16mb, 64mb, 256mb, ... (1mb to 1gb)")
			}
			size = n
		}
		out = append(out, Tmpfs{Path: p, SizeBytes: size})
	}
	return out
}

func validateCapabilities(verr *ValidationError, c capabilitiesRaw) *[]string {
	if !c.given {
		return nil
	}
	expected := "none, or a list of the ones to keep out of " + strings.Join(DefaultCapabilities, ", ")
	kept := []string{}
	switch {
	case c.names == nil && strings.TrimSpace(c.word) == CapabilitiesNone:
	case c.names == nil:
		verr.add("security.capabilities", fmt.Sprintf("invalid value %q", c.word), expected)
	case len(c.names) == 0:
		verr.add("security.capabilities", "must not be empty; write none to keep no capability, or omit it to keep Docker's default set", expected)
	}
	for i, written := range c.names {
		name := strings.TrimPrefix(strings.ToUpper(strings.TrimSpace(written)), "CAP_")
		switch {
		case !slices.Contains(DefaultCapabilities, name):
			verr.add(fmt.Sprintf("security.capabilities[%d]", i),
				fmt.Sprintf("invalid value %q: not in Docker's default set, and nothing is added to it", written), expected)
		case slices.Contains(kept, name):
			verr.add(fmt.Sprintf("security.capabilities[%d]", i), fmt.Sprintf("%q is listed twice", name), "each capability once")
		default:
			kept = append(kept, name)
		}
	}
	slices.Sort(kept)
	return &kept
}

// securityDocument is the block as documentOf hands it back to the decoder.
func securityDocument(s *Security) any {
	type entry struct {
		Path string `yaml:"path"`
		Size string `yaml:"size"`
	}
	doc := struct {
		ReadOnly     bool    `yaml:"read_only,omitempty"`
		Tmpfs        []entry `yaml:"tmpfs,omitempty"`
		Capabilities any     `yaml:"capabilities,omitempty"`
		NonRoot      bool    `yaml:"non_root,omitempty"`
	}{ReadOnly: s.ReadOnly, NonRoot: s.NonRoot}
	for _, t := range s.Tmpfs {
		doc.Tmpfs = append(doc.Tmpfs, entry{Path: t.Path, Size: strconv.FormatInt(t.SizeBytes, 10)})
	}
	if c := s.Capabilities; c != nil {
		if len(*c) == 0 {
			doc.Capabilities = CapabilitiesNone
		} else {
			doc.Capabilities = *c
		}
	}
	return doc
}

// securityNodes is the block as Document writes it.
func (w *documentWriter) securityNodes(s *Security) *yaml.Node {
	var block []*yaml.Node
	if s.ReadOnly {
		block = append(block, pair("read_only", boolean(true))...)
	}
	if len(s.Tmpfs) > 0 {
		list := &yaml.Node{Kind: yaml.SequenceNode}
		for _, t := range s.Tmpfs {
			if t.SizeBytes == DefaultTmpfsBytes {
				list.Content = append(list.Content, w.str(t.Path))
				continue
			}
			list.Content = append(list.Content, mapping(pair("path", w.str(t.Path)), pair("size", plain(memoryText(t.SizeBytes)))))
		}
		block = append(block, pair("tmpfs", list)...)
	}
	if c := s.Capabilities; c != nil {
		if len(*c) == 0 {
			block = append(block, pair("capabilities", plain(CapabilitiesNone))...)
		} else {
			names := &yaml.Node{Kind: yaml.SequenceNode, Style: yaml.FlowStyle}
			for _, name := range *c {
				names.Content = append(names.Content, plain(name))
			}
			block = append(block, pair("capabilities", names)...)
		}
	}
	if s.NonRoot {
		block = append(block, pair("non_root", boolean(true))...)
	}
	return mapping(block)
}
