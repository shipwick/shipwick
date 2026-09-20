// Package cliconfig resolves how deployctl reaches the agent: its URL and
// API token.
package cliconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"

	"go.yaml.in/yaml/v3"
)

const (
	EnvURL   = "SHIPWICK_AGENT_URL"
	EnvToken = "SHIPWICK_AGENT_TOKEN"
	// EnvConfig overrides the config file location.
	EnvConfig = "SHIPWICK_CONFIG"

	// DefaultURL suits both an agent on this machine and one reached through
	// an SSH tunnel (ssh -L 9000:127.0.0.1:9000 user@server).
	DefaultURL = "http://127.0.0.1:9000"
)

// Config is the content of the config file written by `deployctl login`.
type Config struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// Path returns the config file location: $SHIPWICK_CONFIG, or
// <user config dir>/shipwick/config.yaml.
func Path(getenv func(string) string) (string, error) {
	if p := getenv(EnvConfig); p != "" {
		return p, nil
	}
	dir, err := os.UserConfigDir()
	if err != nil {
		return "", fmt.Errorf("locate config directory: %w", err)
	}
	return filepath.Join(dir, "shipwick", "config.yaml"), nil
}

// Load reads the config file. A missing file is an empty config, not an error.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	} else if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var cfg Config
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	return cfg, nil
}

// Save writes the config file, readable by the current user only: it holds a
// credential equivalent to root access on the server.
func Save(path string, cfg Config) error {
	data, err := yaml.Marshal(cfg)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return fmt.Errorf("create config directory: %w", err)
	}
	// Write-then-rename, so a crash never leaves a half-written credential
	// file, and so the permissions apply even if the file already existed.
	tmp, err := os.CreateTemp(filepath.Dir(path), ".config-*.yaml")
	if err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	defer os.Remove(tmp.Name())
	if err := tmp.Chmod(0o600); err != nil && !errors.Is(err, errors.ErrUnsupported) {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if _, err := tmp.Write(data); err != nil {
		tmp.Close()
		return fmt.Errorf("write config: %w", err)
	}
	if err := tmp.Close(); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return fmt.Errorf("write config: %w", err)
	}
	return nil
}

// Resolve determines the effective URL and token. Precedence, highest first:
// the --url flag, environment variables, the config file, the default URL.
//
// There is deliberately no --token flag: command-line arguments are visible
// to every user on the machine (ps) and are kept in shell history.
//
// The saved token belongs to the saved URL. If a flag or variable points
// deployctl at a different agent, the saved token is not sent there.
func Resolve(flagURL string, getenv func(string) string, file Config) Config {
	savedURL := file.URL
	if savedURL == "" {
		savedURL = DefaultURL
	}

	out := Config{URL: savedURL}
	if v := getenv(EnvURL); v != "" {
		out.URL = v
	}
	if flagURL != "" {
		out.URL = flagURL
	}

	if v := getenv(EnvToken); v != "" {
		out.Token = v
	} else if out.URL == savedURL {
		out.Token = file.Token
	}
	return out
}
