// Package config loads the agent's configuration from environment variables.
package config

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io/fs"
	"log/slog"
	"net"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"

	"github.com/shipwick/shipwick/pkg/spec"
)

const (
	EnvToken       = "SHIPWICK_AGENT_TOKEN"
	EnvListenAddr  = "SHIPWICK_LISTEN_ADDR"
	EnvDataDir     = "SHIPWICK_DATA_DIR"
	EnvNetwork     = "SHIPWICK_DOCKER_NETWORK"
	EnvLogLevel    = "SHIPWICK_LOG_LEVEL"
	EnvLogFormat   = "SHIPWICK_LOG_FORMAT"
	EnvCaddyAdmin  = "SHIPWICK_CADDY_ADMIN"
	EnvAgentDomain = "SHIPWICK_AGENT_DOMAIN"

	EnvDashboardDomain   = "SHIPWICK_DASHBOARD_DOMAIN"
	EnvDashboardUpstream = "SHIPWICK_DASHBOARD_UPSTREAM"
)

// MinTokenLength rejects tokens that are trivially guessable.
const MinTokenLength = 16

const tokenHashFile = "agent-token.sha256"

type Config struct {
	// ListenAddr defaults to loopback: exposing the API is an explicit choice.
	ListenAddr string
	DataDir    string
	Network    string
	LogLevel   slog.Level
	LogFormat  string // text | json
	// CaddyAdmin is the reverse proxy's admin endpoint: "unix//path/admin.sock"
	// or "http://host:port". Empty disables routing.
	CaddyAdmin string
	// AgentDomain, when set, serves the agent's own API on that hostname
	// through the proxy, which is how it gets HTTPS.
	AgentDomain string
	// DashboardDomain, when set, serves the dashboard at DashboardUpstream
	// ("host:port", reachable from the proxy) on that hostname.
	DashboardDomain   string
	DashboardUpstream string
	// Token is the raw value of SHIPWICK_AGENT_TOKEN, if set. It is consumed
	// by ResolveToken and must never be logged.
	Token string
}

func (c Config) DatabasePath() string {
	return filepath.Join(c.DataDir, "shipwick.db")
}

// Load reads the configuration. getenv is os.Getenv in production.
func Load(getenv func(string) string) (Config, error) {
	cfg := Config{
		ListenAddr:  valueOr(getenv(EnvListenAddr), "127.0.0.1:9000"),
		DataDir:     valueOr(getenv(EnvDataDir), defaultDataDir()),
		Network:     valueOr(getenv(EnvNetwork), "shipwick"),
		LogFormat:   valueOr(strings.ToLower(getenv(EnvLogFormat)), "text"),
		Token:       getenv(EnvToken),
		CaddyAdmin:  getenv(EnvCaddyAdmin),
		AgentDomain: strings.ToLower(strings.TrimSpace(getenv(EnvAgentDomain))),

		DashboardDomain:   strings.ToLower(strings.TrimSpace(getenv(EnvDashboardDomain))),
		DashboardUpstream: valueOr(getenv(EnvDashboardUpstream), "dashboard:3000"),
	}
	if cfg.LogFormat != "text" && cfg.LogFormat != "json" {
		return Config{}, fmt.Errorf("%s: invalid value %q (expected text or json)", EnvLogFormat, cfg.LogFormat)
	}
	if lvl := getenv(EnvLogLevel); lvl != "" {
		if err := cfg.LogLevel.UnmarshalText([]byte(lvl)); err != nil {
			return Config{}, fmt.Errorf("%s: invalid value %q (expected debug, info, warn or error)", EnvLogLevel, lvl)
		}
	}
	if cfg.Token != "" && len(cfg.Token) < MinTokenLength {
		return Config{}, fmt.Errorf("%s must be at least %d characters long", EnvToken, MinTokenLength)
	}
	if cfg.AgentDomain != "" {
		if err := spec.ValidateDomain(cfg.AgentDomain); err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvAgentDomain, err)
		}
		if cfg.CaddyAdmin == "" {
			return Config{}, fmt.Errorf("%s needs a reverse proxy to be served by: set %s as well", EnvAgentDomain, EnvCaddyAdmin)
		}
	}
	if cfg.DashboardDomain != "" {
		if err := spec.ValidateDomain(cfg.DashboardDomain); err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvDashboardDomain, err)
		}
		if cfg.CaddyAdmin == "" {
			return Config{}, fmt.Errorf("%s needs a reverse proxy to be served by: set %s as well", EnvDashboardDomain, EnvCaddyAdmin)
		}
		if cfg.DashboardDomain == cfg.AgentDomain {
			return Config{}, fmt.Errorf("%s and %s must be different hostnames", EnvDashboardDomain, EnvAgentDomain)
		}
		// It goes into the proxy configuration as a dial address: a plain
		// host:port and nothing else.
		host, port, err := net.SplitHostPort(cfg.DashboardUpstream)
		if err != nil || spec.ValidateDomain(strings.ToLower(host)) != nil && net.ParseIP(host) == nil {
			return Config{}, fmt.Errorf("%s: invalid value %q (expected host:port)", EnvDashboardUpstream, cfg.DashboardUpstream)
		}
		if n, err := strconv.Atoi(port); err != nil || n < 1 || n > 65535 {
			return Config{}, fmt.Errorf("%s: invalid port in %q", EnvDashboardUpstream, cfg.DashboardUpstream)
		}
	}
	return cfg, nil
}

func valueOr(v, fallback string) string {
	if v == "" {
		return fallback
	}
	return v
}

func defaultDataDir() string {
	if runtime.GOOS == "linux" {
		return "/var/lib/shipwick"
	}
	if dir, err := os.UserConfigDir(); err == nil {
		return filepath.Join(dir, "shipwick")
	}
	return "shipwick-data"
}

// ResolveToken determines the SHA-256 hash the API authenticates against.
// The agent only ever keeps the hash, in memory and on disk.
//
//  1. SHIPWICK_AGENT_TOKEN, when set, always wins.
//  2. Otherwise the hash persisted in the data directory by a previous run.
//  3. Otherwise a token is generated and only its hash is persisted. The
//     token itself is returned as `generated` so the caller can show it to the
//     operator exactly once; it cannot be recovered afterwards.
func ResolveToken(cfg Config) (hash [sha256.Size]byte, generated string, err error) {
	if cfg.Token != "" {
		return sha256.Sum256([]byte(cfg.Token)), "", nil
	}

	path := filepath.Join(cfg.DataDir, tokenHashFile)
	data, err := os.ReadFile(path)
	switch {
	case err == nil:
		raw, decodeErr := hex.DecodeString(strings.TrimSpace(string(data)))
		if decodeErr != nil || len(raw) != sha256.Size {
			return hash, "", fmt.Errorf("%s is corrupt; delete it to generate a new token, or set %s", path, EnvToken)
		}
		copy(hash[:], raw)
		return hash, "", nil
	case !errors.Is(err, fs.ErrNotExist):
		return hash, "", fmt.Errorf("read token hash: %w", err)
	}

	secret := make([]byte, 32)
	if _, err := rand.Read(secret); err != nil {
		return hash, "", fmt.Errorf("generate token: %w", err)
	}
	generated = "shw_" + hex.EncodeToString(secret)
	hash = sha256.Sum256([]byte(generated))

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return hash, "", fmt.Errorf("create data directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(hash[:])+"\n"), 0o600); err != nil {
		return hash, "", fmt.Errorf("persist token hash: %w", err)
	}
	return hash, generated, nil
}
