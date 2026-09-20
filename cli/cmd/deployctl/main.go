// Command deployctl is the Shipwick command-line client.
package main

import (
	"context"
	"fmt"
	"os"
	"os/signal"

	"github.com/shipwick/shipwick/cli/internal/commands"
)

func main() {
	// Ctrl+C cancels the command in flight. For `deploy` that only stops the
	// waiting: the deployment itself carries on in the agent.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt)
	defer stop()

	root := commands.NewRootCommand(commands.Options{
		In:     os.Stdin,
		Out:    os.Stdout,
		Err:    os.Stderr,
		Getenv: os.Getenv,
	})
	if err := root.ExecuteContext(ctx); err != nil {
		if msg := commands.Render(err); msg != "" {
			fmt.Fprintln(os.Stderr, msg)
		}
		stop()
		os.Exit(1)
	}
}
