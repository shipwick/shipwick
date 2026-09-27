package commands

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/shipwick/shipwick/pkg/spec"
)

// describeArgv shows a command line for reading, never for running: arguments
// with spaces in them are quoted so that their boundaries stay visible.
func describeArgv(args []string) string {
	parts := make([]string, len(args))
	for i, arg := range args {
		if arg == "" || strings.ContainsAny(arg, " \t\"") {
			arg = strconv.Quote(arg)
		}
		parts[i] = arg
	}
	return strings.Join(parts, " ")
}

func describeLogging(l spec.Logging) string {
	switch n := len(l.Options); n {
	case 0:
		return l.Driver
	case 1:
		return l.Driver + " (1 option)"
	default:
		return fmt.Sprintf("%s (%d options)", l.Driver, n)
	}
}
