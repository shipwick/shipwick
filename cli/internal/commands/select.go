package commands

import (
	"errors"
	"fmt"
	"os"
	"slices"
	"strings"

	"github.com/spf13/cobra"

	"github.com/shipwick/shipwick/pkg/spec"
)

// `shipwick deploy api web` deploys those applications of a shipwick.yaml
// and nothing else: what a pipeline wants after it built one image, and what
// keeps a change to one application from redeploying the database next to
// it. The whole file is still read and validated, so a name is checked
// against everything it describes.

// selection is the applications of a shipwick.yaml a command was asked for.
type selection struct {
	entries []spec.Entry
	// index is where each entry stands in the file: what was learned about
	// the file application by application is looked up by it.
	index []int
	// assumed names the applications that were left out and that a chosen one
	// is written to wait for, in file order. Nothing is known about them
	// here; they are taken to be running.
	assumed []string
}

// selectEntries narrows entries to the named ones, in the order of the file.
// `after` keeps ordering the chosen among themselves; an `after` that names
// an application left out is dropped and reported in assumed. No name at all
// chooses every application.
func selectEntries(file string, entries []spec.Entry, names []string) (selection, error) {
	if len(names) == 0 {
		index := make([]int, len(entries))
		for i := range entries {
			index[i] = i
		}
		return selection{entries: entries, index: index}, nil
	}

	all := make([]string, len(entries))
	for i, e := range entries {
		all[i] = e.App.Name
	}
	var unknown []string
	for _, name := range names {
		if !slices.Contains(all, name) && !slices.Contains(unknown, name) {
			unknown = append(unknown, name)
		}
	}
	if len(unknown) > 0 {
		return selection{}, fmt.Errorf("%s has no application named %s\n\nIt describes: %s", file, strings.Join(unknown, ", "), strings.Join(all, ", "))
	}

	var s selection
	left := map[string]bool{}
	for i, e := range entries {
		if !slices.Contains(names, e.App.Name) {
			continue
		}
		// The entry is the caller's; its list is not edited in place.
		var after []string
		for _, dep := range e.After {
			if slices.Contains(names, dep) {
				after = append(after, dep)
			} else {
				left[dep] = true
			}
		}
		e.After = after
		s.entries = append(s.entries, e)
		s.index = append(s.index, i)
	}
	for _, name := range all {
		if left[name] {
			s.assumed = append(s.assumed, name)
		}
	}
	return s, nil
}

// assumedLine says which applications were not waited for; "" when every
// `after` is among the chosen.
func (s selection) assumedLine() string {
	if len(s.assumed) == 0 {
		return ""
	}
	return fmt.Sprintf("Not deployed now and assumed to be running: %s", strings.Join(s.assumed, ", "))
}

// withImage is --image for a selection: it applies when exactly one
// application was named, to that one.
func (s *selection) withImage(image string) error {
	if len(s.entries) != 1 {
		return fmt.Errorf("--image applies to one application, and %d are named\n\nName the one it is for: shipwick deploy <name> --image %s", len(s.entries), image)
	}
	e := &s.entries[0]
	if e.App.Build != nil {
		return fmt.Errorf("%s has build: and is built here, so --image does not apply; remove build: to deploy an image instead", e.App.Name)
	}
	config := overrideImage(e.Config, image)
	app, err := spec.Parse(config)
	if err != nil {
		return err
	}
	e.Config, e.App = config, app
	return nil
}

// refuseNames explains why a command that was given names of applications
// has nothing to choose them from: only a shipwick.yaml describes several.
func refuseNames(cmd *cobra.Command, files []string) error {
	what, again := DefaultFile+" describes one application and takes no name", "shipwick "+cmd.Name()
	switch {
	case !cmd.Flags().Changed("file"):
		if _, err := os.Stat(DefaultFile); errors.Is(err, os.ErrNotExist) {
			return fmt.Errorf("a name chooses among the applications of a %s, and there is none here\n\nRun it where the file is, or name the file with -f", spec.MultiFile)
		}
	case len(files) == 1:
		what, again = files[0]+" describes one application and takes no name", again+" -f "+files[0]
	default:
		what, again = "the files given with -f describe one application each and take no name", again+" -f "+strings.Join(files, " -f ")
	}
	return fmt.Errorf("%s: a name chooses among the applications of a %s\n\nLeave the name out: %s", what, spec.MultiFile, again)
}
