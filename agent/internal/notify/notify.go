// Package notify tells the outside world what happened to an application: a
// deployment succeeded or failed, an application went down or came back, an
// alert was raised or cleared. The one channel is a webhook; the engine only
// knows the Notifier interface.
package notify

import (
	"bytes"
	"context"
	"crypto/hmac"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/shipwick/shipwick/pkg/outbound"
	"github.com/shipwick/shipwick/pkg/version"
)

// Event kinds. Nothing else is ever sent: a notification is for something the
// operator would act on, not a copy of the event log.
const (
	DeploymentSucceeded  = "deployment.succeeded"
	DeploymentFailed     = "deployment.failed"
	DeploymentRolledBack = "deployment.rolled_back"
	ApplicationDown      = "application.down"
	ApplicationRecovered = "application.recovered"
	JobFailed            = "job.failed"
	// An alert is a condition rather than an occurrence: raised once when it
	// becomes true, cleared once when it no longer is.
	AlertRaised         = "alert.raised"
	AlertCleared        = "alert.cleared"
	CertificateExpiring = "certificate.expiring"
)

// Event is one thing worth telling. Message is a complete sentence for a
// human — what happened and what to do — and is all that chat webhooks get;
// the other fields let a program act on the generic payload.
type Event struct {
	Kind         string
	Application  string
	DeploymentID int64 // 0 when the event is not about one deployment
	Version      string
	Message      string
	At           time.Time
	Server       string // hostname of the server the agent runs on
	// Alert says which condition an alert.* event is about; nil for the rest.
	Alert *Alert
}

// Alert identifies the condition behind an alert.* event. Application is in
// the event; Replica is 0 when the condition is not about one replica.
type Alert struct {
	Kind     string `json:"kind"`     // memory | disk | restarts | unhealthy
	Severity string `json:"severity"` // warning | critical
	Replica  int    `json:"replica"`
}

// Notifier delivers events. Notify must return at once: it is called from
// the deployment engine and the supervisor, which have better things to do
// than wait for a chat service.
type Notifier interface {
	Notify(ctx context.Context, e Event)
}

// ValidateURL checks a webhook URL without ever repeating it: the URL is a
// credential (Slack's and Discord's carry the token in the path), so the
// error must be safe to log and to show. Plain http is allowed only towards
// the machine itself or a private network, where nobody else can read it.
func ValidateURL(raw string) error {
	u, err := url.Parse(raw)
	if err != nil || u.Host == "" || u.Hostname() == "" {
		return errors.New("must be an absolute URL such as https://hooks.example.com/shipwick")
	}
	switch u.Scheme {
	case "https":
	case "http":
		if !isLocal(u.Hostname()) {
			return errors.New("must use https; http is allowed only for localhost and private addresses")
		}
	default:
		return errors.New("must use https")
	}
	return nil
}

func isLocal(host string) bool {
	if host == "localhost" {
		return true
	}
	ip := net.ParseIP(host)
	return ip != nil && (ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast())
}

// Options configure a Webhook.
type Options struct {
	URL string
	// Secret, when set, signs every request: X-Shipwick-Signature carries
	// "sha256=" and the hex HMAC-SHA256 of the body.
	Secret string
	// Server names the host in the payload; empty means os.Hostname.
	Server string
	Logger *slog.Logger
}

const (
	queueSize      = 256
	attemptTimeout = 10 * time.Second
	drainTimeout   = 5 * time.Second
)

// Webhook posts events to one URL. Events are queued and sent by a single
// goroutine, in order, so a slow or dead endpoint never holds up the engine:
// when the queue is full, the event is dropped and the drop is logged. A
// failed delivery is retried after 1s, 5s and 25s, then given up.
type Webhook struct {
	url    *url.URL
	host   string // the only part of the URL that is ever logged
	secret string
	server string
	format func(Event) any
	log    *slog.Logger
	client *http.Client

	backoff []time.Duration
	drain   time.Duration

	mu     sync.Mutex
	closed bool
	queue  chan Event

	ctx    context.Context // cancelled by Close once the drain has taken too long
	cancel context.CancelFunc
	done   chan struct{}
}

func NewWebhook(opts Options) (*Webhook, error) {
	if err := ValidateURL(opts.URL); err != nil {
		return nil, err
	}
	u, _ := url.Parse(opts.URL)
	if opts.Server == "" {
		opts.Server, _ = os.Hostname()
	}
	if opts.Logger == nil {
		opts.Logger = slog.Default()
	}
	ctx, cancel := context.WithCancel(context.Background())
	w := &Webhook{
		url:     u,
		host:    u.Hostname(),
		secret:  opts.Secret,
		server:  opts.Server,
		format:  formatFor(u),
		log:     opts.Logger,
		client:  &http.Client{Timeout: attemptTimeout, Transport: outbound.Transport()},
		backoff: []time.Duration{time.Second, 5 * time.Second, 25 * time.Second},
		drain:   drainTimeout,
		queue:   make(chan Event, queueSize),
		ctx:     ctx,
		cancel:  cancel,
		done:    make(chan struct{}),
	}
	go w.run()
	return w, nil
}

// Host is the webhook's hostname, for logs and status output.
func (w *Webhook) Host() string { return w.host }

// Notify queues the event and returns. The context is the caller's and is
// not used for delivery: a deployment's context is usually already dead by
// the time its outcome is known.
func (w *Webhook) Notify(_ context.Context, e Event) {
	if e.At.IsZero() {
		e.At = time.Now()
	}
	if e.Server == "" {
		e.Server = w.server
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		w.log.Warn("notification dropped: the webhook is closed", "event", e.Kind, "app", e.Application, "host", w.host)
		return
	}
	select {
	case w.queue <- e:
	default:
		w.log.Warn("notification dropped: the webhook queue is full", "event", e.Kind, "app", e.Application, "host", w.host)
	}
}

// Close delivers what is queued, for a bounded time, and stops the sender.
// Retries that are still pending when the bound is reached are abandoned.
func (w *Webhook) Close() {
	w.mu.Lock()
	if w.closed {
		w.mu.Unlock()
		<-w.done
		return
	}
	w.closed = true
	close(w.queue)
	w.mu.Unlock()

	deadline := time.NewTimer(w.drain)
	defer deadline.Stop()
	select {
	case <-w.done:
	case <-deadline.C:
		w.cancel()
		<-w.done
	}
}

func (w *Webhook) run() {
	defer close(w.done)
	for e := range w.queue {
		w.deliver(e)
	}
}

func (w *Webhook) deliver(e Event) {
	body, err := json.Marshal(w.format(e))
	if err != nil {
		w.log.Error("could not encode notification", "event", e.Kind, "error", err)
		return
	}
	for attempt := 1; ; attempt++ {
		err := w.post(body)
		if err == nil {
			return
		}
		var status *statusError
		if errors.As(err, &status) && !status.retryable() {
			w.log.Warn("notification rejected", "event", e.Kind, "app", e.Application, "host", w.host, "error", err)
			return
		}
		if attempt > len(w.backoff) || w.ctx.Err() != nil {
			w.log.Warn("notification not delivered", "event", e.Kind, "app", e.Application, "host", w.host, "attempts", attempt, "error", err)
			return
		}
		wait := time.NewTimer(w.backoff[attempt-1])
		select {
		case <-wait.C:
		case <-w.ctx.Done():
			wait.Stop()
			w.log.Warn("notification not delivered", "event", e.Kind, "app", e.Application, "host", w.host, "attempts", attempt, "error", err)
			return
		}
	}
}

func (w *Webhook) post(body []byte) error {
	req, err := http.NewRequestWithContext(w.ctx, http.MethodPost, w.url.String(), bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("User-Agent", "shipwick-agent/"+version.Version)
	if w.secret != "" {
		req.Header.Set("X-Shipwick-Signature", Sign(w.secret, body))
	}
	resp, err := w.client.Do(req)
	if err != nil {
		// url.Error repeats the full URL, token included.
		var urlErr *url.Error
		if errors.As(err, &urlErr) {
			err = urlErr.Err
		}
		return err
	}
	resp.Body.Close()
	if resp.StatusCode/100 != 2 {
		return &statusError{code: resp.StatusCode}
	}
	return nil
}

// Sign computes the value of the X-Shipwick-Signature header for a body.
func Sign(secret string, body []byte) string {
	mac := hmac.New(sha256.New, []byte(secret))
	mac.Write(body)
	return "sha256=" + hex.EncodeToString(mac.Sum(nil))
}

type statusError struct{ code int }

func (e *statusError) Error() string { return fmt.Sprintf("HTTP %d", e.code) }

// retryable: a server that is down or rate-limiting may recover within the
// retry window; a request it has rejected outright will be rejected again.
func (e *statusError) retryable() bool {
	return e.code >= 500 || e.code == http.StatusTooManyRequests
}

// formatFor picks the payload shape by destination. Chat services take one
// text field and render nothing else; everything else gets the full event.
func formatFor(u *url.URL) func(Event) any {
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "hooks.slack.com":
		return func(e Event) any { return slackMessage{Text: e.Message} }
	case (host == "discord.com" || host == "discordapp.com") && strings.HasPrefix(u.Path, "/api/webhooks/"):
		return func(e Event) any { return discordMessage{Content: e.Message} }
	}
	return func(e Event) any { return newPayload(e) }
}

type slackMessage struct {
	Text string `json:"text"`
}

type discordMessage struct {
	Content string `json:"content"`
}

// Payload is the generic JSON body: the event's fields, with the same
// sentence the chat formats carry.
type Payload struct {
	Event        string    `json:"event"`
	Application  string    `json:"application"`
	DeploymentID *int64    `json:"deployment_id"`
	Version      string    `json:"version"`
	Message      string    `json:"message"`
	At           time.Time `json:"at"`
	Server       string    `json:"server"`
	// Alert is present on alert.raised and alert.cleared only.
	Alert *Alert `json:"alert,omitempty"`
}

func newPayload(e Event) Payload {
	p := Payload{
		Event:       e.Kind,
		Application: e.Application,
		Version:     e.Version,
		Message:     e.Message,
		At:          e.At.UTC().Truncate(time.Second),
		Server:      e.Server,
		Alert:       e.Alert,
	}
	if e.DeploymentID != 0 {
		p.DeploymentID = &e.DeploymentID
	}
	return p
}
