package commands

import (
	"context"
	"fmt"
	"regexp"
	"strings"

	"github.com/shipwick/shipwick/cli/internal/client"
	"github.com/shipwick/shipwick/pkg/spec"
	"github.com/shipwick/shipwick/pkg/version"
)

// describeSecurity is the Security line of `shipwick validate`: what the
// containers go without, always in the same order.
func describeSecurity(s spec.Security) string {
	var parts []string
	if s.ReadOnly {
		parts = append(parts, "read-only root filesystem")
	}
	if len(s.Tmpfs) > 0 {
		paths := make([]string, len(s.Tmpfs))
		for i, t := range s.Tmpfs {
			paths[i] = fmt.Sprintf("%s (%s)", t.Path, spec.FormatMemory(t.SizeBytes))
		}
		parts = append(parts, "tmpfs at "+strings.Join(paths, ", "))
	}
	if c := s.Capabilities; c != nil {
		if len(*c) == 0 {
			parts = append(parts, "no capabilities")
		} else {
			parts = append(parts, "capabilities "+strings.Join(*c, ", ")+" only")
		}
	}
	if s.NonRoot {
		parts = append(parts, "refuses to run as root")
	}
	return strings.Join(parts, "; ")
}

var unknownFieldPattern = regexp.MustCompile(`^unknown field "([^"]+)"$`)

// explainUnknownKeys adds, to an agent's refusal of a document this shipwick
// accepted, what the refusal means. The two read deploy.yaml by the same
// rules, so a key only the agent does not know is a key newer than the agent.
// The agent deploys nothing from such a document — an application is never
// left running without what the key asks for — but "unknown field" alone
// reads like a typo.
func explainUnknownKeys(ctx context.Context, cl *client.Client, err error) error {
	verr, ok := client.ValidationError(err)
	if !ok {
		return err
	}
	var keys []string
	for _, f := range verr.Fields {
		if m := unknownFieldPattern.FindStringSubmatch(f.Message); m != nil {
			keys = append(keys, m[1])
		}
	}
	if len(keys) == 0 {
		return err
	}

	agent := "The agent"
	if info, ierr := cl.Server(ctx); ierr == nil && info.AgentVersion != "" {
		agent = "The agent is version " + info.AgentVersion + " and"
	}
	return &olderAgentError{report: verr.Error(), explanation: fmt.Sprintf(
		"%s does not know %s, which this shipwick (%s) does: the server is older than deploy.yaml.\nNothing was deployed. Upgrade the server, then deploy again:\n  %s (on the server)",
		agent, quotedList(keys), version.Version, installerCommand)}
}

// olderAgentError is an agent's report on a document, with what it means
// under it.
type olderAgentError struct{ report, explanation string }

func (e *olderAgentError) Error() string { return e.report + "\n" + e.explanation }

func quotedList(items []string) string {
	quoted := make([]string, len(items))
	for i, item := range items {
		quoted[i] = `"` + item + `"`
	}
	return strings.Join(quoted, ", ")
}
