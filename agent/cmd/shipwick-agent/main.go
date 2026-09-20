// Command shipwick-agent is the Shipwick server component. It manages
// application containers through the Docker Engine API and exposes the
// HTTP API used by deployctl and the dashboard.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"runtime"
	"syscall"
	"time"

	"github.com/shipwick/shipwick/agent/internal/api"
	"github.com/shipwick/shipwick/agent/internal/config"
	"github.com/shipwick/shipwick/agent/internal/deploy"
	"github.com/shipwick/shipwick/agent/internal/docker"
	"github.com/shipwick/shipwick/agent/internal/proxy"
	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/version"
)

const (
	startupTimeout  = 30 * time.Second
	shutdownTimeout = 30 * time.Second
)

func main() {
	var err error
	switch {
	case len(os.Args) == 1:
		err = run()
	case len(os.Args) == 2 && os.Args[1] == "healthcheck":
		err = healthcheck()
	case len(os.Args) == 2 && os.Args[1] == "version":
		fmt.Println(version.Version)
	default:
		err = fmt.Errorf("unknown arguments %q\n\nusage: shipwick-agent [healthcheck|version]\nThe agent is configured through SHIPWICK_* environment variables", os.Args[1:])
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "shipwick-agent:", err)
		os.Exit(1)
	}
}

// healthcheck probes the agent configured by the same environment. It exists
// for container health checks: the agent image ships no shell, curl or wget.
func healthcheck() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	host, port, err := net.SplitHostPort(cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("%s: %w", config.EnvListenAddr, err)
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}

	client := &http.Client{Timeout: 3 * time.Second}
	resp, err := client.Get("http://" + net.JoinHostPort(host, port) + "/api/v1/health")
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("unhealthy: HTTP %d", resp.StatusCode)
	}
	return nil
}

func run() error {
	cfg, err := config.Load(os.Getenv)
	if err != nil {
		return err
	}
	log := newLogger(cfg)
	slog.SetDefault(log)

	// First SIGINT/SIGTERM starts a graceful shutdown; a second one kills
	// the process the default way, since stop() restores default handling.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	tokenHash, generated, err := config.ResolveToken(cfg)
	if err != nil {
		return err
	}
	if generated != "" {
		printGeneratedToken(generated)
	}

	startCtx, cancelStart := context.WithTimeout(ctx, startupTimeout)
	defer cancelStart()

	st, err := store.Open(startCtx, cfg.DatabasePath())
	if err != nil {
		return err
	}
	defer st.Close()

	rt, err := docker.New(cfg.Network)
	if err != nil {
		return err
	}
	defer rt.Close()
	if err := rt.Ping(startCtx); err != nil {
		return fmt.Errorf("%w\n\nIs Docker running, and may this user access its socket?", err)
	}

	if err := rt.EnsureNetwork(startCtx); err != nil {
		return err
	}
	// Health checks are HTTP requests to container addresses, so the agent
	// has to be able to reach the application network.
	attached, err := rt.AttachSelf(startCtx)
	switch {
	case err != nil:
		log.Warn("could not join the application network; health checks may fail", "network", cfg.Network, "error", err)
	case attached:
		log.Info("agent container is on the application network", "network", cfg.Network)
	case runtime.GOOS != "linux":
		log.Warn("the agent runs outside Docker on " + runtime.GOOS + ", where container networks are unreachable from the host: " +
			"applications with a health check will fail to deploy. Run the agent in a container (docker compose up)")
	}

	opts := deploy.Options{Logger: log}
	if cfg.CaddyAdmin != "" {
		caddy, err := proxy.NewCaddy(cfg.CaddyAdmin)
		if err != nil {
			return fmt.Errorf("%s: %w", config.EnvCaddyAdmin, err)
		}
		opts.Proxy = caddy
		if cfg.AgentDomain != "" {
			upstream, err := ownAddress(cfg.ListenAddr, attached)
			if err != nil {
				return err
			}
			// Streaming: log following must not be buffered by the proxy.
			opts.ExtraRoutes = append(opts.ExtraRoutes, proxy.Route{Domain: cfg.AgentDomain, Upstreams: []string{upstream}, Streaming: true})
		}
		if cfg.DashboardDomain != "" {
			// Streaming for the same reason: the dashboard relays followed logs.
			opts.ExtraRoutes = append(opts.ExtraRoutes, proxy.Route{Domain: cfg.DashboardDomain, Upstreams: []string{cfg.DashboardUpstream}, Streaming: true})
		}
	} else {
		log.Warn("no reverse proxy configured: applications with a domain will not be reachable", "set", config.EnvCaddyAdmin)
	}

	engine := deploy.New(st, rt, opts)
	if err := engine.Recover(startCtx); err != nil {
		return fmt.Errorf("recover state: %w", err)
	}
	// Caddy may have been restarted, or replaced, while the agent was away.
	// Not fatal: applications keep running, and the supervisor retries.
	if err := engine.SyncProxy(startCtx); err != nil {
		log.Error("could not configure the reverse proxy; retrying in the background", "error", err)
	}
	// After Recover: the supervisor must only ever see settled state.
	engine.StartSupervisor()

	apiServer := api.New(engine, tokenHash, log)
	srv := &http.Server{
		Handler:           apiServer.Handler(),
		ReadHeaderTimeout: 10 * time.Second,
		ReadTimeout:       30 * time.Second,
		WriteTimeout:      60 * time.Second,
		IdleTimeout:       2 * time.Minute,
		MaxHeaderBytes:    64 * 1024,
	}
	listener, err := net.Listen("tcp", cfg.ListenAddr)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", cfg.ListenAddr, err)
	}

	serveErr := make(chan error, 1)
	go func() { serveErr <- srv.Serve(listener) }()
	log.Info("shipwick agent started",
		"version", version.Version,
		"addr", listener.Addr().String(),
		"data_dir", cfg.DataDir,
		"network", cfg.Network,
	)

	select {
	case err := <-serveErr:
		return fmt.Errorf("http server: %w", err)
	case <-ctx.Done():
	}
	stop()
	log.Info("shutting down")

	shutdownCtx, cancel := context.WithTimeout(context.Background(), shutdownTimeout)
	defer cancel()
	// Order matters: end log streams (Shutdown would wait for them), stop
	// accepting requests, settle in-flight deployments, and only then
	// (deferred) close Docker and the database.
	apiServer.Close()
	httpErr := srv.Shutdown(shutdownCtx)
	if errors.Is(httpErr, http.ErrServerClosed) {
		httpErr = nil
	}
	engineErr := engine.Shutdown(shutdownCtx)

	log.Info("shutdown complete")
	return errors.Join(httpErr, engineErr)
}

// ownAddress is where the reverse proxy can reach this agent. In a container
// on the application network that is the container's hostname, which Docker's
// DNS resolves there; as a host process, the proxy is assumed to be a host
// process too, and loopback is the only address the API listens on by default.
func ownAddress(listenAddr string, inContainer bool) (string, error) {
	host, port, err := net.SplitHostPort(listenAddr)
	if err != nil {
		return "", fmt.Errorf("%s: %w", config.EnvListenAddr, err)
	}
	if inContainer {
		hostname, err := os.Hostname()
		if err != nil {
			return "", fmt.Errorf("determine own hostname: %w", err)
		}
		return net.JoinHostPort(hostname, port), nil
	}
	if ip := net.ParseIP(host); host == "" || (ip != nil && ip.IsUnspecified()) {
		host = "127.0.0.1"
	}
	return net.JoinHostPort(host, port), nil
}

func newLogger(cfg config.Config) *slog.Logger {
	opts := &slog.HandlerOptions{Level: cfg.LogLevel}
	if cfg.LogFormat == "json" {
		return slog.New(slog.NewJSONHandler(os.Stderr, opts))
	}
	return slog.New(slog.NewTextHandler(os.Stderr, opts))
}

// printGeneratedToken shows a freshly generated token exactly once. It goes
// to stdout directly, never through the logger, so it cannot end up in log
// aggregation.
func printGeneratedToken(token string) {
	fmt.Printf(`
  Shipwick generated an API token for this agent:

      %s

  Store it now: only its hash is kept, so it cannot be shown again.
  If this output is being collected (docker logs, journald), the token is in
  those logs too: prefer setting %s yourself.
  Use it with deployctl and the dashboard as %s.
  To choose your own token instead, set %s.

`, token, config.EnvToken, config.EnvToken, config.EnvToken)
}
