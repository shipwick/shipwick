// Package cliconfig resolves how shipwick reaches the agent: its URL and
// API token, per saved server ("context").
package cliconfig

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"go.yaml.in/yaml/v3"
)

const (
	EnvURL   = "SHIPWICK_AGENT_URL"
	EnvToken = "SHIPWICK_AGENT_TOKEN"
	// EnvContext selects a saved server, like --context.
	EnvContext = "SHIPWICK_CONTEXT"
	// EnvConfig overrides the config file location.
	EnvConfig = "SHIPWICK_CONFIG"

	// DefaultURL suits both an agent on this machine and one reached through
	// an SSH tunnel (ssh -L 9000:127.0.0.1:9000 user@server).
	DefaultURL = "http://127.0.0.1:9000"

	// DefaultContext names the context a first `shipwick login` creates, and
	// the one a single-server config file from before contexts existed becomes.
	DefaultContext = "default"
)

// Context is one saved server: where its agent is and the token for it.
type Context struct {
	URL   string `yaml:"url"`
	Token string `yaml:"token"`
}

// Config is the content of the config file written by `shipwick login`.
type Config struct {
	Current  string             `yaml:"current,omitempty"`
	Contexts map[string]Context `yaml:"contexts,omitempty"`
}

// Names lists the contexts, sorted.
func (c Config) Names() []string {
	names := make([]string, 0, len(c.Contexts))
	for name := range c.Contexts {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}

// Set adds or replaces a context and makes it the current one.
func (c *Config) Set(name string, ctx Context) {
	if c.Contexts == nil {
		c.Contexts = map[string]Context{}
	}
	c.Contexts[name] = ctx
	c.Current = name
}

// Remove drops a context. When it was the current one, no context is current
// afterwards: guessing another server is not what someone removing one wants.
func (c *Config) Remove(name string) {
	delete(c.Contexts, name)
	if c.Current == name {
		c.Current = ""
	}
}

// Target is what a command connects to: the resolved URL and token, and the
// name of the context they came from ("" when a flag or variable chose the URL).
type Target struct {
	URL     string
	Token   string
	Context string
}

// ErrUnknownContext is returned when --context or SHIPWICK_CONTEXT names a
// context that is not in the config file.
var ErrUnknownContext = errors.New("unknown context")

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
//
// Files written before contexts existed hold one server as top-level `url` and
// `token`; they load as the context "default", and the next Save writes the
// current format.
func Load(path string) (Config, error) {
	data, err := os.ReadFile(path)
	if errors.Is(err, fs.ErrNotExist) {
		return Config{}, nil
	} else if err != nil {
		return Config{}, fmt.Errorf("read %s: %w", path, err)
	}
	var raw struct {
		Config `yaml:",inline"`
		URL    string `yaml:"url"`
		Token  string `yaml:"token"`
	}
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return Config{}, fmt.Errorf("parse %s: %w", path, err)
	}
	cfg := raw.Config
	if len(cfg.Contexts) == 0 && (raw.URL != "" || raw.Token != "") {
		cfg.Set(DefaultContext, Context{URL: raw.URL, Token: raw.Token})
	}
	return cfg, nil
}

// Save writes the config file, readable by the current user only: it holds
// credentials equivalent to root access on the servers.
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

// ContextName decides which saved server a command means: --context, then
// SHIPWICK_CONTEXT, then the current one. It may name a context that does
// not exist yet; Resolve rejects that, login creates it.
func ContextName(flagContext string, getenv func(string) string, file Config) string {
	if flagContext != "" {
		return flagContext
	}
	if v := getenv(EnvContext); v != "" {
		return v
	}
	return file.Current
}

// Resolve determines the effective URL and token. Precedence, highest first:
// the --url flag, environment variables, the selected context, the default
// URL.
//
// There is deliberately no --token flag: command-line arguments are visible
// to every user on the machine (ps) and are kept in shell history.
//
// The saved token belongs to the saved URL. If a flag or variable points
// shipwick at a different agent, the saved token is not sent there.
func Resolve(flagURL, flagContext string, getenv func(string) string, file Config) (Target, error) {
	name := ContextName(flagContext, getenv, file)
	saved, ok := file.Contexts[name]
	if name != "" && !ok {
		return Target{}, fmt.Errorf("%w %q\n\nSee the saved ones with: shipwick context ls", ErrUnknownContext, name)
	}
	if saved.URL == "" {
		saved.URL = DefaultURL
	}

	out := Target{URL: saved.URL}
	if v := getenv(EnvURL); v != "" {
		out.URL = v
	}
	if flagURL != "" {
		out.URL = flagURL
	}

	if out.URL == saved.URL {
		out.Token = saved.Token
		if ok {
			out.Context = name
		}
	}
	if v := getenv(EnvToken); v != "" {
		out.Token = v
	}
	return out, nil
}
