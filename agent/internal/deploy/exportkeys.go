package deploy

import (
	"encoding"
	"encoding/json"
	"fmt"
	"reflect"
	"sort"
	"strings"

	"github.com/shipwick/shipwick/pkg/spec"
)

// An export is read by agents older than the one that wrote it, and a JSON
// key the reader does not know is skipped by its decoder without a word: an
// application would be deployed without the block that asked for less. So
// export.json names the keys a reader must know, `requires`: every key of
// the applications' configuration the export uses, as a path in app.json
// (`spec.security.read_only`). The list is derived from what is written, not
// kept by hand, and the reader compares it with the paths its own types
// have, so a key added to pkg/spec is covered on both sides without being
// named anywhere. An export written before the list existed carries none,
// and nothing of it is refused.

// requiredKeys lists the key paths the configurations use, in order.
func requiredKeys(configurations []spec.App) ([]string, error) {
	present := map[string]bool{}
	for _, a := range configurations {
		if err := keyPaths(a, reflect.TypeOf(a), "spec", present); err != nil {
			return nil, fmt.Errorf("the keys %s uses: %w", a.Name, err)
		}
	}
	return sorted(present), nil
}

// unknownKeys returns the paths of requires that this agent's app.json does
// not have.
func unknownKeys(requires []string) []string {
	known := map[string]bool{}
	knownKeyPaths(reflect.TypeOf(exportApp{}), "", known, nil)
	var unknown []string
	for _, path := range requires {
		if !known[path] {
			unknown = append(unknown, path)
		}
	}
	return unknown
}

// keyPaths records the paths of the keys present in the JSON of v, decoded
// as t: struct fields at every depth, as dotted paths from prefix. The keys
// of a map are values of the format, not keys of it, and nothing below one
// is a path; nor is anything below a type that marshals itself.
func keyPaths(v any, t reflect.Type, prefix string, out map[string]bool) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return rawKeyPaths(raw, t, prefix, out)
}

func rawKeyPaths(raw json.RawMessage, t reflect.Type, prefix string, out map[string]bool) error {
	t = indirect(t)
	if t == nil || marshalsItself(t) || string(raw) == "null" {
		return nil
	}
	switch t.Kind() {
	case reflect.Struct:
		var object map[string]json.RawMessage
		if err := json.Unmarshal(raw, &object); err != nil {
			return err
		}
		for name, field := range jsonFields(t) {
			member, ok := object[name]
			if !ok {
				continue
			}
			path := joinPath(prefix, name)
			out[path] = true
			if err := rawKeyPaths(member, field.Type, path, out); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		var elements []json.RawMessage
		if err := json.Unmarshal(raw, &elements); err != nil {
			return err
		}
		for _, element := range elements {
			if err := rawKeyPaths(element, t.Elem(), prefix, out); err != nil {
				return err
			}
		}
	}
	return nil
}

// knownKeyPaths records every path a value of t can have, the way
// rawKeyPaths would list it. seen guards against a type that contains
// itself, which none of the export's does.
func knownKeyPaths(t reflect.Type, prefix string, out map[string]bool, seen []reflect.Type) {
	t = indirect(t)
	if t == nil || marshalsItself(t) {
		return
	}
	for _, s := range seen {
		if s == t {
			return
		}
	}
	seen = append(seen, t)
	switch t.Kind() {
	case reflect.Struct:
		for name, field := range jsonFields(t) {
			path := joinPath(prefix, name)
			out[path] = true
			knownKeyPaths(field.Type, path, out, seen)
		}
	case reflect.Slice, reflect.Array:
		knownKeyPaths(t.Elem(), prefix, out, seen)
	}
}

// jsonFields is the exported fields of a struct by the name encoding/json
// writes them under.
func jsonFields(t reflect.Type) map[string]reflect.StructField {
	fields := map[string]reflect.StructField{}
	for i := 0; i < t.NumField(); i++ {
		f := t.Field(i)
		if !f.IsExported() {
			continue
		}
		name, _, _ := strings.Cut(f.Tag.Get("json"), ",")
		switch name {
		case "-":
			continue
		case "":
			name = f.Name
		}
		fields[name] = f
	}
	return fields
}

func indirect(t reflect.Type) reflect.Type {
	for t != nil && t.Kind() == reflect.Pointer {
		t = t.Elem()
	}
	return t
}

var (
	jsonMarshaler = reflect.TypeOf((*json.Marshaler)(nil)).Elem()
	textMarshaler = reflect.TypeOf((*encoding.TextMarshaler)(nil)).Elem()
)

func marshalsItself(t reflect.Type) bool {
	p := reflect.PointerTo(t)
	return t.Implements(jsonMarshaler) || p.Implements(jsonMarshaler) ||
		t.Implements(textMarshaler) || p.Implements(textMarshaler)
}

func joinPath(prefix, name string) string {
	if prefix == "" {
		return name
	}
	return prefix + "." + name
}

func sorted(set map[string]bool) []string {
	out := make([]string, 0, len(set))
	for path := range set {
		out = append(out, path)
	}
	sort.Strings(out)
	return out
}
