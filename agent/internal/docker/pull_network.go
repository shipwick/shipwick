package docker

import (
	"context"
	"errors"
	"strings"

	"github.com/moby/moby/client"
)

// Images are pulled by the Docker daemon, with the daemon's own proxy and the
// daemon's own certificate authorities; nothing the agent is configured with
// reaches a pull. A pull that fails on the way to the registry therefore ends
// in advice about the daemon, which the daemon's own message — a dial error
// inside a dial error — does not give.

// pullAdvice is the sentence for a pull that did not get as far as an answer
// from the registry: "" when err is not of that kind. daemonProxy says
// whether the daemon has a proxy configured.
func pullAdvice(err error, registry string, daemonProxy bool) string {
	msg := strings.ToLower(err.Error())
	switch {
	case containsAny(msg, "x509:", "certificate signed by unknown authority", "failed to verify certificate"):
		return "The Docker daemon does not trust the certificate " + registry + " answered with. " +
			"Images are pulled by the daemon, not by the agent: put the certificate of the authority that issued it in /etc/docker/certs.d/" + registry + "/ca.crt on the server" +
			" (behind a proxy that opens TLS, that is the proxy's authority)"
	case !containsAny(msg, "dial tcp", "proxyconnect", "i/o timeout", "tls handshake timeout", "connection refused", "connection reset",
		"network is unreachable", "no route to host", "no such host", "server misbehaving", "client.timeout exceeded",
		"proxy authentication required") && !strings.HasSuffix(msg, ": forbidden"):
		return ""
	case daemonProxy:
		return "The Docker daemon could not reach " + registry + " through the proxy it is configured with. " +
			"Check \"proxies\" in /etc/docker/daemon.json on the server, and that the proxy allows " + registry
	}
	return "The Docker daemon could not reach " + registry + ". " +
		"Images are pulled by the daemon, not by the agent: behind a proxy, set \"proxies\" in /etc/docker/daemon.json on the server and restart Docker; " +
		"on a server with no way out, deploy with build: in deploy.yaml, which sends the image through the agent"
}

func containsAny(s string, signs ...string) bool {
	for _, sign := range signs {
		if strings.Contains(s, sign) {
			return true
		}
	}
	return false
}

// explainPull adds pullAdvice to a failed pull. The daemon is asked about its
// proxy only now, when the answer changes what the operator is told.
func (r *Runtime) explainPull(ctx context.Context, image string, err error) error {
	if err == nil || ctx.Err() != nil || errors.Is(err, ErrPullDenied) {
		return err
	}
	registry, _, rerr := ImageRegistry(image)
	if rerr != nil {
		return err
	}
	daemonProxy := false
	if res, ierr := r.cli.Info(ctx, client.InfoOptions{}); ierr == nil {
		daemonProxy = res.Info.HTTPProxy != "" || res.Info.HTTPSProxy != ""
	}
	advice := pullAdvice(err, registry, daemonProxy)
	if advice == "" {
		return err
	}
	return &PullUnreachableError{Err: err, Advice: advice}
}

// PullUnreachableError is a pull that failed before the registry answered,
// with what to do about it.
type PullUnreachableError struct {
	Err    error
	Advice string
}

func (e *PullUnreachableError) Error() string { return e.Err.Error() + ". " + e.Advice }
func (e *PullUnreachableError) Unwrap() error { return e.Err }
