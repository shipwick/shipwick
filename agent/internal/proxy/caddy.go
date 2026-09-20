package proxy

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"strings"
	"sync"
	"time"
)

// verifyEvery is how often an unchanged config is re-checked against what
// Caddy actually runs. Caddy restarting with an older config is rare, but the
// consequence — every application unreachable until the next deployment —
// is not acceptable.
const verifyEvery = 10 * time.Second

// Status is a snapshot of the proxy for the API.
type Status struct {
	Enabled   bool
	Reachable bool
	Error     string
	Routes    int
}

// Caddy applies routes through Caddy's admin API.
type Caddy struct {
	adminListen string // as Caddy spells it: "unix//run/caddy/admin.sock", "localhost:2019"
	baseURL     string
	http        *http.Client

	mu          sync.Mutex
	applied     string // fingerprint of the config last loaded
	verifiedAt  time.Time
	routes      int
	lastErr     error
	everReached bool
}

// NewCaddy creates a client for the admin endpoint at addr, given either as
// "unix//path/to/admin.sock" (preferred: only processes sharing the socket
// can reconfigure the proxy) or as "http://host:port".
func NewCaddy(addr string) (*Caddy, error) {
	c := &Caddy{}
	transport := &http.Transport{Proxy: nil, MaxIdleConns: 2, IdleConnTimeout: 30 * time.Second}

	switch {
	case strings.HasPrefix(addr, "unix/"):
		path := strings.TrimPrefix(addr, "unix/")
		if !strings.HasPrefix(path, "/") || len(path) < 2 {
			return nil, fmt.Errorf("invalid Caddy admin address %q: expected unix//absolute/path.sock", addr)
		}
		transport.DialContext = func(ctx context.Context, _, _ string) (net.Conn, error) {
			return (&net.Dialer{}).DialContext(ctx, "unix", path)
		}
		c.adminListen = addr
		// The host is irrelevant for dialing; Caddy accepts this one for
		// unix-socket admin endpoints.
		c.baseURL = "http://127.0.0.1"
	case strings.HasPrefix(addr, "http://"):
		u, err := url.Parse(addr)
		if err != nil || u.Host == "" || (u.Path != "" && u.Path != "/") {
			return nil, fmt.Errorf("invalid Caddy admin address %q: expected http://host:port", addr)
		}
		c.adminListen = u.Host
		c.baseURL = "http://" + u.Host
	default:
		return nil, fmt.Errorf("invalid Caddy admin address %q: expected unix//path/admin.sock or http://host:port", addr)
	}

	c.http = &http.Client{Transport: transport, Timeout: 15 * time.Second}
	return c, nil
}

// Sync makes Caddy serve exactly routes. It is cheap to call repeatedly: a
// config identical to the one already running is not loaded again.
func (c *Caddy) Sync(ctx context.Context, routes []Route) error {
	c.mu.Lock()
	defer c.mu.Unlock()

	err := c.sync(ctx, routes)
	if ctx.Err() == nil { // a cancelled context says nothing about Caddy
		c.lastErr = err
	}
	if err == nil {
		c.everReached, c.routes = true, len(routes)
	}
	return err
}

func (c *Caddy) sync(ctx context.Context, routes []Route) error {
	config, fingerprint, err := Build(c.adminListen, routes)
	if err != nil {
		return fmt.Errorf("build proxy config: %w", err)
	}

	if fingerprint == c.applied {
		if time.Since(c.verifiedAt) < verifyEvery {
			return nil
		}
		running, err := c.isRunning(ctx, fingerprint)
		if err != nil {
			return err
		}
		if running {
			c.verifiedAt = time.Now()
			return nil
		}
		// Caddy lost our config (restarted with an older one): load it again.
	}

	if err := c.load(ctx, config); err != nil {
		return err
	}
	c.applied, c.verifiedAt = fingerprint, time.Now()
	return nil
}

// Status reports the outcome of the most recent Sync.
func (c *Caddy) Status() Status {
	c.mu.Lock()
	defer c.mu.Unlock()
	s := Status{Enabled: true, Reachable: c.everReached && c.lastErr == nil, Routes: c.routes}
	if c.lastErr != nil {
		s.Error = c.lastErr.Error()
	}
	return s
}

func (c *Caddy) load(ctx context.Context, config []byte) error {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.baseURL+"/load", bytes.NewReader(config))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	resp, err := c.http.Do(req)
	if err != nil {
		return unreachable(err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 2048))
		return fmt.Errorf("caddy rejected the configuration (HTTP %d): %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	return nil
}

func (c *Caddy) isRunning(ctx context.Context, fingerprint string) (bool, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.baseURL+"/id/"+markerPrefix+fingerprint, nil)
	if err != nil {
		return false, err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return false, unreachable(err)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))
	return resp.StatusCode == http.StatusOK, nil
}

func unreachable(err error) error {
	var urlErr *url.Error
	if errors.As(err, &urlErr) {
		err = urlErr.Err
	}
	return fmt.Errorf("cannot reach Caddy's admin endpoint: %w", err)
}
