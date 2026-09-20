// Package health implements the HTTP health check run against replicas.
package health

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strconv"
	"syscall"
	"time"
)

// Checker probes replicas over HTTP. A replica is healthy when it answers 2xx.
type Checker struct {
	client *http.Client
}

func New() *Checker {
	return &Checker{client: &http.Client{
		Transport: &http.Transport{
			// Targets are container IPs on a private network: an HTTP proxy
			// from the agent's environment must never sit in between.
			Proxy: nil,
			// Every probe opens its own connection. A kept-alive connection
			// can outlive the listener it was accepted by and report a
			// wedged application as healthy.
			DisableKeepAlives: true,
		},
		// A redirect is an answer, not a 2xx. Following it could also walk
		// the probe out of the container network.
		CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse },
	}}
}

// Check performs one probe: GET http://ip:port/path, bounded by timeout.
// The returned error is short and meant to be shown to users.
func (c *Checker) Check(ctx context.Context, ip string, port int, path string, timeout time.Duration) error {
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	url := "http://" + net.JoinHostPort(ip, strconv.Itoa(port)) + path
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return fmt.Errorf("invalid health check URL: %w", err)
	}
	req.Header.Set("User-Agent", "shipwick-health-check")

	resp, err := c.client.Do(req)
	if err != nil {
		return describe(err, timeout)
	}
	defer resp.Body.Close()
	io.Copy(io.Discard, io.LimitReader(resp.Body, 64*1024))

	if resp.StatusCode < 200 || resp.StatusCode > 299 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	return nil
}

// describe reduces Go's layered network errors to the fact a user needs.
func describe(err error, timeout time.Duration) error {
	switch {
	case errors.Is(err, context.DeadlineExceeded) || os.IsTimeout(err):
		return fmt.Errorf("no response within %s", timeout)
	case errors.Is(err, context.Canceled):
		return context.Canceled
	case errors.Is(err, syscall.ECONNREFUSED):
		return errors.New("connection refused")
	case errors.Is(err, syscall.ECONNRESET), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF):
		return errors.New("connection closed before a response")
	}
	var netErr *net.OpError
	if errors.As(err, &netErr) {
		return fmt.Errorf("%s failed: %v", netErr.Op, netErr.Err)
	}
	return err
}
