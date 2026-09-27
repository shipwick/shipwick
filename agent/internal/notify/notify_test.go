package notify

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestValidateURL(t *testing.T) {
	ok := []string{
		"https://hooks.slack.com/services/T000/B000/XXXX",
		"https://discord.com/api/webhooks/1/abc",
		"http://localhost:8080/hook",
		"http://127.0.0.1/hook",
		"http://10.0.0.5:9000/hook",
		"http://192.168.1.20/hook",
		"http://[::1]/hook",
	}
	for _, u := range ok {
		if err := ValidateURL(u); err != nil {
			t.Errorf("%s: unexpected error: %v", u, err)
		}
	}
	bad := map[string]string{
		"http to the internet":   "http://hooks.example.com/hook",
		"http to a public IP":    "http://8.8.8.8/hook",
		"http to a private name": "http://hooks.internal/hook",
		"no scheme":              "hooks.example.com/hook",
		"other scheme":           "ftp://hooks.example.com/hook",
		"empty":                  "",
	}
	for name, u := range bad {
		err := ValidateURL(u)
		if err == nil {
			t.Errorf("%s: %q should be rejected", name, u)
			continue
		}
		if u != "" && strings.Contains(err.Error(), u) {
			t.Errorf("%s: the error repeats the URL, which is a credential: %v", name, err)
		}
	}
}

// sink is a webhook endpoint under the test's control.
type sink struct {
	srv *httptest.Server

	mu       sync.Mutex
	requests []*http.Request
	bodies   [][]byte
	// failures is how many requests are still to be answered with status.
	failures int
	status   int
	received chan struct{} // one message per request handled
	// block, when set, holds every request until it is closed.
	block chan struct{}
}

func newSink(t *testing.T) *sink {
	s := &sink{received: make(chan struct{}, 64)}
	s.srv = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if s.block != nil {
			<-s.block
		}
		body, _ := io.ReadAll(r.Body)
		s.mu.Lock()
		s.requests = append(s.requests, r)
		s.bodies = append(s.bodies, body)
		fail := s.failures > 0
		if fail {
			s.failures--
		}
		s.mu.Unlock()
		if fail {
			w.WriteHeader(s.status)
		}
		s.received <- struct{}{}
	}))
	t.Cleanup(s.srv.Close)
	return s
}

// wait blocks until n requests have been handled.
func (s *sink) wait(t *testing.T, n int) {
	t.Helper()
	for i := 0; i < n; i++ {
		select {
		case <-s.received:
		case <-time.After(5 * time.Second):
			t.Fatalf("only %d of %d requests arrived", i, n)
		}
	}
}

func (s *sink) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.requests)
}

func newTestWebhook(t *testing.T, rawURL, secret string, logs io.Writer) *Webhook {
	t.Helper()
	if logs == nil {
		logs = io.Discard
	}
	w, err := NewWebhook(Options{URL: rawURL, Secret: secret, Server: "vps-1", Logger: slog.New(slog.NewTextHandler(logs, nil))})
	if err != nil {
		t.Fatalf("NewWebhook: %v", err)
	}
	w.backoff = []time.Duration{time.Millisecond, time.Millisecond, time.Millisecond}
	t.Cleanup(w.Close)
	return w
}

var sample = Event{
	Kind: DeploymentSucceeded, Application: "my-api", DeploymentID: 42, Version: "1.4.2",
	Message: "my-api is running 1.4.2", At: time.Date(2026, 3, 1, 10, 0, 0, 0, time.UTC),
}

func TestGenericPayloadCarriesTheWholeEvent(t *testing.T) {
	s := newSink(t)
	w := newTestWebhook(t, s.srv.URL+"/hook", "", nil)
	w.Notify(context.Background(), sample)
	s.wait(t, 1)

	req, body := s.requests[0], s.bodies[0]
	if req.Method != http.MethodPost || req.Header.Get("Content-Type") != "application/json" {
		t.Errorf("request = %s %s", req.Method, req.Header.Get("Content-Type"))
	}
	if req.Header.Get("X-Shipwick-Signature") != "" {
		t.Error("no secret, no signature")
	}
	var got map[string]any
	if err := json.Unmarshal(body, &got); err != nil {
		t.Fatalf("body %s: %v", body, err)
	}
	want := map[string]any{
		"event": "deployment.succeeded", "application": "my-api", "deployment_id": float64(42), "version": "1.4.2",
		"message": "my-api is running 1.4.2", "at": "2026-03-01T10:00:00Z", "server": "vps-1",
	}
	for k, v := range want {
		if got[k] != v {
			t.Errorf("%s = %v, want %v", k, got[k], v)
		}
	}
	if len(got) != len(want) {
		t.Errorf("payload has fields %v, want exactly %v", got, want)
	}
}

func TestPayloadShapeFollowsTheHost(t *testing.T) {
	tests := []struct {
		name, url, want string
	}{
		{"slack", "https://hooks.slack.com/services/T0/B0/x", `{"text":"my-api is running 1.4.2"}`},
		{"discord", "https://discord.com/api/webhooks/1/abc", `{"content":"my-api is running 1.4.2"}`},
		{"discordapp", "https://discordapp.com/api/webhooks/1/abc", `{"content":"my-api is running 1.4.2"}`},
		{"discord without the webhook path", "https://discord.com/hook", `"event":"deployment.succeeded"`},
		{"anything else", "https://hooks.example.com/shipwick", `"event":"deployment.succeeded"`},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			u, _ := url.Parse(tt.url)
			body, err := json.Marshal(formatFor(u)(sample))
			if err != nil {
				t.Fatal(err)
			}
			if !strings.Contains(string(body), tt.want) {
				t.Errorf("body = %s, want it to contain %s", body, tt.want)
			}
		})
	}
}

func TestSignatureIsTheHMACOfTheBody(t *testing.T) {
	s := newSink(t)
	w := newTestWebhook(t, s.srv.URL+"/hook", "s3cret", nil)
	w.Notify(context.Background(), sample)
	s.wait(t, 1)

	got := s.requests[0].Header.Get("X-Shipwick-Signature")
	if want := Sign("s3cret", s.bodies[0]); got != want || !strings.HasPrefix(got, "sha256=") {
		t.Errorf("signature = %q, want %q", got, want)
	}
	if got == Sign("other", s.bodies[0]) {
		t.Error("the signature does not depend on the secret")
	}
}

func TestDeliveryIsRetried(t *testing.T) {
	s := newSink(t)
	s.failures, s.status = 2, http.StatusBadGateway
	w := newTestWebhook(t, s.srv.URL+"/hook", "", nil)
	w.Notify(context.Background(), sample)
	s.wait(t, 3)
	if n := s.count(); n != 3 {
		t.Errorf("requests = %d; two failures and then the one that succeeded", n)
	}
	// One delivery, however many attempts: the third answer was the success.
	w.Notify(context.Background(), sample)
	s.wait(t, 1)
	if n := s.count(); n != 4 {
		t.Errorf("requests = %d; a delivered event must not be sent again", n)
	}
}

func TestRejectedDeliveryIsNotRetried(t *testing.T) {
	s := newSink(t)
	s.failures, s.status = 5, http.StatusNotFound
	logs := &bytes.Buffer{}
	w := newTestWebhook(t, s.srv.URL+"/secret-token", "", logs)
	w.Notify(context.Background(), sample)
	s.wait(t, 1)
	// A second event proves the first was given up rather than still retrying.
	w.Notify(context.Background(), sample)
	s.wait(t, 1)
	if n := s.count(); n != 2 {
		t.Errorf("requests = %d; a 404 will not turn into a 200 by asking again", n)
	}
	w.Close()
	if !strings.Contains(logs.String(), "HTTP 404") {
		t.Errorf("the rejection should be logged:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "secret-token") {
		t.Errorf("the log repeats the URL, which is a credential:\n%s", logs.String())
	}
}

func TestGivenUpAfterTheRetriesLogsOnlyTheHost(t *testing.T) {
	s := newSink(t)
	s.failures, s.status = 10, http.StatusInternalServerError
	logs := &bytes.Buffer{}
	w := newTestWebhook(t, s.srv.URL+"/secret-token", "", logs)
	w.Notify(context.Background(), sample)
	s.wait(t, 4) // the attempt and three retries
	w.Close()

	if !strings.Contains(logs.String(), "notification not delivered") || !strings.Contains(logs.String(), "attempts=4") {
		t.Errorf("giving up should be logged with the attempt count:\n%s", logs.String())
	}
	if strings.Contains(logs.String(), "secret-token") || !strings.Contains(logs.String(), "host=127.0.0.1") {
		t.Errorf("logs must name the host and nothing more of the URL:\n%s", logs.String())
	}
}

func TestNotifyNeverBlocksOnASlowEndpoint(t *testing.T) {
	s := newSink(t)
	s.block = make(chan struct{})
	logs := &bytes.Buffer{}
	w := newTestWebhook(t, s.srv.URL+"/hook", "", logs)
	w.drain = time.Millisecond

	// The first event is stuck in flight; the queue then fills up and the
	// rest is dropped. None of these calls may wait for the endpoint.
	returned := make(chan struct{})
	go func() {
		for i := 0; i < queueSize+5; i++ {
			w.Notify(context.Background(), sample)
		}
		close(returned)
	}()
	select {
	case <-returned:
	case <-time.After(5 * time.Second):
		t.Fatal("Notify blocked on a slow endpoint")
	}
	if !strings.Contains(logs.String(), "queue is full") {
		t.Errorf("dropped events must be logged:\n%s", logs.String())
	}

	// Close is bounded too: the stuck request is abandoned.
	closed := make(chan struct{})
	go func() {
		w.Close()
		close(closed)
	}()
	select {
	case <-closed:
	case <-time.After(5 * time.Second):
		t.Fatal("Close waited for a stuck endpoint")
	}
	close(s.block)
	w.Notify(context.Background(), sample)
	if !strings.Contains(logs.String(), "webhook is closed") {
		t.Errorf("an event after Close is dropped and logged:\n%s", logs.String())
	}
}

func TestCloseDeliversWhatIsQueued(t *testing.T) {
	s := newSink(t)
	w := newTestWebhook(t, s.srv.URL+"/hook", "", nil)
	for i := 0; i < 3; i++ {
		w.Notify(context.Background(), sample)
	}
	w.Close()
	if n := s.count(); n != 3 {
		t.Errorf("requests = %d after Close, want 3", n)
	}
}

func TestEventsArriveInOrder(t *testing.T) {
	s := newSink(t)
	w := newTestWebhook(t, s.srv.URL+"/hook", "", nil)
	for _, kind := range []string{DeploymentFailed, ApplicationDown, ApplicationRecovered} {
		e := sample
		e.Kind = kind
		w.Notify(context.Background(), e)
	}
	s.wait(t, 3)
	var kinds []string
	for _, body := range s.bodies {
		var p Payload
		json.Unmarshal(body, &p)
		kinds = append(kinds, p.Event)
	}
	if got := strings.Join(kinds, " "); got != "deployment.failed application.down application.recovered" {
		t.Errorf("order = %s", got)
	}
}

func TestNotifyFillsTimeAndServer(t *testing.T) {
	s := newSink(t)
	w := newTestWebhook(t, s.srv.URL+"/hook", "", nil)
	w.Notify(context.Background(), Event{Kind: ApplicationDown, Application: "my-api", Message: "my-api is down"})
	s.wait(t, 1)
	var p Payload
	json.Unmarshal(s.bodies[0], &p)
	if p.At.IsZero() || p.Server != "vps-1" || p.DeploymentID != nil {
		t.Errorf("payload = %+v; at and server are filled in, deployment_id stays null", p)
	}
}
