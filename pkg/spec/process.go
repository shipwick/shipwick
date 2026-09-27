package spec

import (
	"fmt"
	"regexp"
	"strings"

	"go.yaml.in/yaml/v3"
)

// MaxArgs bounds an entrypoint or command.
const MaxArgs = 64

// argv is a command line in deploy.yaml: a list of arguments, or one string,
// which is one argument. It is never split on spaces; that is what a shell
// does, and there is none.
type argv []string

func (a *argv) UnmarshalYAML(node *yaml.Node) error {
	for node.Kind == yaml.AliasNode {
		node = node.Alias
	}
	switch node.Kind {
	case yaml.ScalarNode:
		*a = argv{node.Value}
		return nil
	case yaml.SequenceNode:
		return node.Decode((*[]string)(a))
	}
	return &yaml.TypeError{Errors: []string{fmt.Sprintf("line %d: expected a string or a list of strings, not a %s",
		node.Line, strings.TrimPrefix(node.ShortTag(), "!!"))}}
}

// userPattern is a name or a numeric id, alone or as user:group — the forms
// an image's USER instruction takes. Names are Unix user names, which is
// what the container's /etc/passwd resolves.
var userPattern = regexp.MustCompile(`^([a-z_][a-z0-9_-]{0,31}|[0-9]{1,10})(:([a-z_][a-z0-9_-]{0,31}|[0-9]{1,10}))?$`)

// validateProcess checks the overrides of the image's entrypoint, command and
// user.
func (r raw) validateProcess(verr *ValidationError) (entrypoint, command []string, user string) {
	entrypoint = validateArgv(verr, "entrypoint", r.Entrypoint, `["dotnet"]`)
	command = validateArgv(verr, "command", r.Command, `["App.dll", "--urls", "http://0.0.0.0:8080"]`)
	user = strings.TrimSpace(r.User)
	if user != "" && !userPattern.MatchString(user) {
		verr.add("user", fmt.Sprintf("invalid value %q", r.User), "a user name or id, with or without a group: app, 1000, 1000:1000")
	}
	return entrypoint, command, user
}

// validateArgv checks one command line. Nil means the key was not given and
// the image's own is kept; an empty list is refused rather than guessed at.
func validateArgv(verr *ValidationError, field string, args []string, example string) []string {
	if args == nil {
		return nil
	}
	switch {
	case len(args) == 0:
		verr.add(field, "must not be empty; omit it to keep the image's own", example)
		return nil
	case len(args) > MaxArgs:
		verr.add(field, fmt.Sprintf("too many arguments (%d)", len(args)), fmt.Sprintf("at most %d", MaxArgs))
		return nil
	}
	for i, arg := range args {
		switch {
		case arg == "":
			verr.add(fmt.Sprintf("%s[%d]", field, i), "must not be empty", example)
		case strings.ContainsAny(arg, "\x00\r\n"):
			verr.add(fmt.Sprintf("%s[%d]", field, i), "must not contain newlines or NUL bytes", "")
		}
	}
	return args
}
