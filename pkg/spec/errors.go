package spec

import (
	"strings"
)

// FieldError describes one problem with one field of deploy.yaml.
type FieldError struct {
	// Field is the dotted path of the offending field, e.g. "resources.memory".
	Field   string `json:"field"`
	Message string `json:"message"`
	// Expected optionally lists examples of accepted values.
	Expected string `json:"expected,omitempty"`
}

// ValidationError aggregates every problem found in a config, so users can
// fix them all in one pass instead of one error per run.
type ValidationError struct {
	Fields []FieldError
}

func (e *ValidationError) add(field, message, expected string) {
	e.Fields = append(e.Fields, FieldError{Field: field, Message: message, Expected: expected})
}

// Error renders the human-readable, multi-line report shown by the CLI:
//
//	invalid deploy.yaml
//
//	resources.memory:
//	  invalid value "abc"
//	  expected: 128mb, 512mb, 1gb, ...
func (e *ValidationError) Error() string {
	var b strings.Builder
	b.WriteString("invalid deploy.yaml\n")
	for _, f := range e.Fields {
		b.WriteString("\n")
		b.WriteString(f.Field)
		b.WriteString(":\n  ")
		b.WriteString(f.Message)
		b.WriteString("\n")
		if f.Expected != "" {
			b.WriteString("  expected: ")
			b.WriteString(f.Expected)
			b.WriteString("\n")
		}
	}
	return b.String()
}
