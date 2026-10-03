package main

import (
	"context"
	"fmt"
	"log/slog"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/config"
	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/pkg/outbound"
)

// useOutbound loads the certificate authorities the operator added, before
// any client is built, and says in the log how the agent leaves the server:
// once, at startup, with the proxy's host and never its URL.
func useOutbound(cfg config.Config, log *slog.Logger) error {
	if err := outbound.Trust(cfg.Outbound.CAFile); err != nil {
		return fmt.Errorf("%s: %w", config.EnvCAFile, err)
	}
	if cfg.Outbound.CAFile != "" {
		log.Info("certificate authorities added to the system's", "file", cfg.Outbound.CAFile)
	}
	if cfg.Outbound.Proxy != "" {
		log.Info("requests that leave the server go through a proxy; images are pulled by the Docker daemon, which has a proxy setting of its own",
			"proxy", cfg.Outbound.Proxy)
	}
	return nil
}

// lookupHost is who the routing gate asks whether a hostname points here.
func lookupHost(cfg config.Config, log *slog.Logger) func(ctx context.Context, host string) ([]string, error) {
	switch {
	case cfg.Outbound.SystemDNS:
		log.Info("hostnames are looked up with the server's own resolver")
		return deploy.SystemLookupHost
	case len(cfg.Outbound.DNSResolvers) > 0:
		log.Info("hostnames are looked up with the name servers given", "servers", strings.Join(cfg.Outbound.DNSResolvers, ", "))
		return deploy.NewResolvers(cfg.Outbound.DNSResolvers).LookupHost
	}
	return deploy.PublicLookupHost
}

// networkOptions is what GET /server reports of all this.
func networkOptions(cfg config.Config) deploy.NetworkOptions {
	opts := deploy.NetworkOptions{
		Proxy:         cfg.Outbound.Proxy,
		CAFile:        cfg.Outbound.CAFile != "",
		DNSResolvers:  cfg.Outbound.DNSResolvers,
		ACMEDirectory: cfg.Outbound.ACMEDirectory,
	}
	if cfg.Outbound.SystemDNS {
		opts.DNSResolvers = []string{config.SystemResolver}
	}
	return opts
}
