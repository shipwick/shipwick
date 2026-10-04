package main

import (
	"log/slog"

	"github.com/shipwick/shipwick/agent/internal/config"
	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/agent/internal/updates"
)

// updateOptions is the agent's daily question about a newer release. It is
// said once, at startup, that the agent asks and how to stop it: a request
// that leaves the server should not be a surprise.
func updateOptions(cfg config.Config, log *slog.Logger) deploy.UpdateOptions {
	if !cfg.UpdateCheck {
		return deploy.UpdateOptions{}
	}
	log.Info("the agent asks github.com once a day whether a newer release exists, and sends nothing about this server",
		"turn off with", config.EnvUpdateCheck+"=off")
	return deploy.UpdateOptions{Latest: updates.New().Latest, StateFile: cfg.UpdateCheckPath()}
}
