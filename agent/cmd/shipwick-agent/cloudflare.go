package main

import (
	"context"
	"os"

	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/pkg/cloudflare"
)

// cloudflareVerifier is the check of the Cloudflare token the agent runs at
// start and when doctor asks. The record it writes and removes in each zone
// names the server, so that a record left behind can be traced to it.
func cloudflareVerifier(ctx context.Context, rt *docker.Runtime, token string) deploy.CloudflareVerifier {
	info, _ := rt.Info(ctx)
	server := info.Hostname
	if server == "" {
		server, _ = os.Hostname()
	}
	return cloudflare.Verifier{Token: token, Server: server}
}
