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
	"regexp"
	"runtime"
	"strconv"
	"strings"

	"github.com/shipwick/shipwick/agent/internal/notify"
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

	EnvEncryptionKey = "SHIPWICK_ENCRYPTION_KEY"
	EnvWebhookURL    = "SHIPWICK_WEBHOOK_URL"
	EnvWebhookSecret = "SHIPWICK_WEBHOOK_SECRET"
)

// Alert thresholds, in percent: see Config.
const (
	EnvAlertMemoryPercent = "SHIPWICK_ALERT_MEMORY_PERCENT"
	EnvAlertDiskPercent   = "SHIPWICK_ALERT_DISK_PERCENT"
)

// Bounds of the alert thresholds. An alert is cleared a few points below
// where it is raised, which a very low threshold would leave no room for;
// and the disk alert turns critical at 95, so its warning must come before.
const (
	minAlertPercent     = 50
	maxAlertDiskPercent = 94
)
const (
	// EnvCloudflareToken is a Cloudflare API token that may read the zone and
	// edit its DNS records. With it the proxy obtains certificates through
	// the DNS challenge, so hostnames can stay behind Cloudflare's proxy.
	EnvCloudflareToken = "SHIPWICK_CLOUDFLARE_API_TOKEN"
)

// cloudflareToken is the form of a Cloudflare API token. The proxy refuses any
// other with an error that quotes the value, and treats braces in it as
// placeholders; neither must be reachable.
var cloudflareToken = regexp.MustCompile(`^([A-Za-z0-9_-]{35,50}|cf(ut|at)_[A-Za-z0-9_-]{32,256})$`)

const (
	// EnvProxyTLSAddr is where the agent reaches the proxy's TLS port, to see
	// the certificates it serves.
	EnvProxyTLSAddr = "SHIPWICK_PROXY_TLS_ADDR"
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
	// EncryptionKey is the raw value of SHIPWICK_ENCRYPTION_KEY, if set: the
	// key environment values are encrypted with, as hex. It is consumed by
	// ResolveEncryptionKey and must never be logged.
	EncryptionKey string
	// WebhookURL, when set, receives a notification for every deployment
	// outcome and every application that goes down or recovers. Like the
	// token, it is a credential and must never be logged; WebhookSecret
	// signs each request.
	WebhookURL    string
	WebhookSecret string
	// AlertMemoryPercent is the share of its memory limit at which a replica
	// raises an alert; AlertDiskPercent how full the disk that holds DataDir
	// may get before it does. Zero: the engine's defaults, 90 and 85.
	AlertMemoryPercent int
	AlertDiskPercent   int
	// CloudflareToken is the raw value of SHIPWICK_CLOUDFLARE_API_TOKEN, if
	// set. It is handed to the proxy inside its configuration and must never
	// be logged.
	CloudflareToken string
	// ProxyTLSAddr is the proxy's TLS port as the agent reaches it, "host:port":
	// the agent connects there to report each hostname's certificate.
	ProxyTLSAddr string
	// Backups says where the agent keeps the backups it takes: see backups.go.
	Backups Backups
	// Transfer is the scheduled export and the standby: see transfer.go.
	Transfer Transfer
	// Outbound is the proxy, the certificate authorities and the name servers
	// of a network that is not the open internet: see outbound.go.
	Outbound Outbound
	// SignIn is the OpenID Connect provider people sign in with; nil: tokens
	// only. See oidc.go.
	SignIn *SignIn
	// Logs is how much output of ended containers is kept: see logs.go.
	Logs LogArchive
	// UpdateCheck is false when the agent must not ask whether a newer
	// release exists: see updates.go.
	UpdateCheck bool
}

func (c Config) DatabasePath() string {
	return filepath.Join(c.DataDir, "shipwick.db")
}

// UploadDir holds the folders of static applications between their upload and
// the deployment that serves them.
func (c Config) UploadDir() string {
	return filepath.Join(c.DataDir, "uploads")
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

		EncryptionKey: strings.TrimSpace(getenv(EnvEncryptionKey)),
		WebhookURL:    strings.TrimSpace(getenv(EnvWebhookURL)),
		WebhookSecret: getenv(EnvWebhookSecret),
	}
	var err error
	if cfg.AlertMemoryPercent, err = percent(getenv, EnvAlertMemoryPercent, 100); err != nil {
		return Config{}, err
	}
	if cfg.AlertDiskPercent, err = percent(getenv, EnvAlertDiskPercent, maxAlertDiskPercent); err != nil {
		return Config{}, err
	}
	cfg.ProxyTLSAddr = valueOr(strings.TrimSpace(getenv(EnvProxyTLSAddr)), "caddy:443")
	if _, _, err := net.SplitHostPort(cfg.ProxyTLSAddr); err != nil {
		return Config{}, fmt.Errorf("%s: invalid value %q (expected host:port)", EnvProxyTLSAddr, cfg.ProxyTLSAddr)
	}
	if err := cfg.loadBackups(getenv); err != nil {
		return Config{}, err
	}
	if err := cfg.loadTransfer(getenv); err != nil {
		return Config{}, err
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
	if cfg.EncryptionKey != "" {
		// The value is a secret: the error names the rule, not the value.
		if _, err := decodeEncryptionKey(cfg.EncryptionKey); err != nil {
			return Config{}, fmt.Errorf("%s: %w (%d random bytes; generate one with: openssl rand -hex %d)", EnvEncryptionKey, err, EncryptionKeySize, EncryptionKeySize)
		}
	}
	if cfg.WebhookURL != "" {
		// The error never repeats the URL: it carries the webhook's token.
		if err := notify.ValidateURL(cfg.WebhookURL); err != nil {
			return Config{}, fmt.Errorf("%s: %w", EnvWebhookURL, err)
		}
	} else if cfg.WebhookSecret != "" {
		return Config{}, fmt.Errorf("%s is set but %s is not", EnvWebhookSecret, EnvWebhookURL)
	}
	cfg.CloudflareToken = strings.TrimSpace(getenv(EnvCloudflareToken))
	if cfg.CloudflareToken != "" {
		// The value is a secret: the error names the rule, not the value.
		if !cloudflareToken.MatchString(cfg.CloudflareToken) {
			return Config{}, fmt.Errorf("%s: not a Cloudflare API token (letters, digits, - and _, at least 35 characters); create one at dash.cloudflare.com → My Profile → API Tokens, not a Global API Key", EnvCloudflareToken)
		}
		if cfg.CaddyAdmin == "" {
			return Config{}, fmt.Errorf("%s needs a reverse proxy to obtain certificates with it: set %s as well", EnvCloudflareToken, EnvCaddyAdmin)
		}
	}
	if err := cfg.loadOutbound(getenv); err != nil {
		return Config{}, err
	}
	if err := cfg.loadSignIn(getenv); err != nil {
		return Config{}, err
	}
	if err := cfg.loadLogArchive(getenv); err != nil {
		return Config{}, err
	}
	if err := cfg.loadUpdateCheck(getenv); err != nil {
		return Config{}, err
	}
	return cfg, nil
}

// percent reads an alert threshold: a whole number of percent, or zero when
// the variable is not set.
func percent(getenv func(string) string, name string, highest int) (int, error) {
	raw := strings.TrimSpace(getenv(name))
	if raw == "" {
		return 0, nil
	}
	n, err := strconv.Atoi(strings.TrimSuffix(raw, "%"))
	if err != nil || n < minAlertPercent || n > highest {
		return 0, fmt.Errorf("%s: invalid value %q (expected a whole number from %d to %d)", name, raw, minAlertPercent, highest)
	}
	return n, nil
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
	generated = "swk_" + hex.EncodeToString(secret)
	hash = sha256.Sum256([]byte(generated))

	if err := os.MkdirAll(cfg.DataDir, 0o700); err != nil {
		return hash, "", fmt.Errorf("create data directory: %w", err)
	}
	if err := os.WriteFile(path, []byte(hex.EncodeToString(hash[:])+"\n"), 0o600); err != nil {
		return hash, "", fmt.Errorf("persist token hash: %w", err)
	}
	return hash, generated, nil
}
