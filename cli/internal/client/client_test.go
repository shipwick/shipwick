package client

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/pkg/api"
)

func newTestClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	c, err := New(srv.URL+"/", "secret-token") // trailing slash must not matter
	if err != nil {
		t.Fatal(err)
	}
	return c
}

func TestRequestShape(t *testing.T) {
	var got *http.Request
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		fmt.Fprint(w, `{"data": []}`)
	})
	if _, err := c.Deployments(context.Background(), "my-api", 5); err != nil {
		t.Fatalf("Deployments: %v", err)
	}
	if got.URL.Path != "/api/v1/deployments" || got.URL.Query().Get("application") != "my-api" || got.URL.Query().Get("limit") != "5" {
		t.Errorf("unexpected URL: %s", got.URL)
	}
	if got.Header.Get("Authorization") != "Bearer secret-token" {
		t.Errorf("Authorization = %q", got.Header.Get("Authorization"))
	}
	if !strings.HasPrefix(got.Header.Get("User-Agent"), "deployctl/") {
		t.Errorf("User-Agent = %q", got.Header.Get("User-Agent"))
	}
}

func TestDeploySendsTheDocumentVerbatim(t *testing.T) {
	const doc = "name: my-api\nimage: nginx:1.27\n"
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, len(doc)+10)
		n, _ := r.Body.Read(body)
		if r.Method != http.MethodPost || r.URL.Path != "/api/v1/applications/my-api/deploy" || string(body[:n]) != doc {
			t.Errorf("unexpected request: %s %s %q", r.Method, r.URL.Path, body[:n])
		}
		w.WriteHeader(http.StatusAccepted)
		fmt.Fprint(w, `{"data": {"id": 7, "sequence": 3, "status": "PENDING"}}`)
	})
	d, err := c.Deploy(context.Background(), "my-api", []byte(doc))
	if err != nil || d.ID != 7 || d.Sequence != 3 || d.Status != api.StatusPending {
		t.Errorf("Deploy = %+v, %v", d, err)
	}
}

func TestAPIErrorsAreDecoded(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusConflict)
		fmt.Fprint(w, `{"error": {"code": "DEPLOYMENT_IN_PROGRESS", "message": "busy", "details": {}}}`)
	})
	_, err := c.Application(context.Background(), "my-api")

	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 409 || apiErr.Message != "busy" {
		t.Fatalf("err = %#v", err)
	}
	if !IsCode(err, api.CodeDeploymentInProgress) || IsCode(err, api.CodeNotFound) {
		t.Error("IsCode mismatch")
	}
}

func TestNonJSONErrorFromAProxy(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprint(w, "<html>502 Bad Gateway</html>")
	})
	_, err := c.Applications(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != 502 || !strings.Contains(apiErr.Message, "502") {
		t.Errorf("err = %v", err)
	}
}

func TestNotAShipwickAgent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { fmt.Fprint(w, "<html>hello</html>") })
	if _, err := c.Health(context.Background()); err == nil || !strings.Contains(err.Error(), "is this a Shipwick agent") {
		t.Errorf("err = %v", err)
	}
}

func TestDeleteHandlesNoContent(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	if err := c.Delete(context.Background(), "my-api"); err != nil {
		t.Errorf("Delete: %v", err)
	}
}

func TestUnreachable(t *testing.T) {
	srv := httptest.NewServer(http.NotFoundHandler())
	srv.Close()
	c, _ := New(srv.URL, "secret-token")

	_, err := c.Health(context.Background())
	var unreachable *UnreachableError
	if !errors.As(err, &unreachable) || unreachable.URL != srv.URL {
		t.Fatalf("err = %#v", err)
	}
	if strings.Contains(err.Error(), "secret-token") || strings.Contains(err.Error(), "/api/v1") {
		t.Errorf("the message should name the agent, not the request: %v", err)
	}
}

func TestCancelledContextIsNotReportedAsUnreachable(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) { <-r.Context().Done() })
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	_, err := c.Health(ctx)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Errorf("err = %v, want the context's own error", err)
	}
}

func TestFollowLogs(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Query().Get("follow") != "true" || r.URL.Query().Get("tail") != "0" {
			t.Errorf("unexpected query: %s", r.URL.RawQuery)
		}
		w.Header().Set("Content-Type", "application/x-ndjson")
		fmt.Fprintln(w, `{"replica": 1, "stream": "stdout", "message": "first"}`)
		w.(http.Flusher).Flush()
		fmt.Fprintln(w, `{"replica": 2, "stream": "stderr", "message": "second"}`)
	})

	var got []api.LogLine
	err := c.FollowLogs(context.Background(), "my-api", 0, func(l api.LogLine) { got = append(got, l) })
	if err != nil {
		t.Fatalf("FollowLogs: %v", err)
	}
	if len(got) != 2 || got[0].Message != "first" || got[1].Replica != 2 {
		t.Errorf("got %+v", got)
	}
}

func TestFollowLogsReportsAPIErrors(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error": {"code": "NOT_FOUND", "message": "not found", "details": {}}}`)
	})
	err := c.FollowLogs(context.Background(), "ghost", 10, func(api.LogLine) {})
	if !IsCode(err, api.CodeNotFound) {
		t.Errorf("err = %v", err)
	}
}

func TestValidationErrorIsRebuilt(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error": {"code": "INVALID_CONFIG", "message": "invalid deploy.yaml", "details": {"fields": [
			{"field": "resources.memory", "message": "invalid value \"abc\"", "expected": "128mb, 512mb, 1gb, ..."}]}}}`)
	})
	_, err := c.Deploy(context.Background(), "my-api", []byte("x"))

	verr, ok := ValidationError(err)
	if !ok || len(verr.Fields) != 1 || verr.Fields[0].Field != "resources.memory" || verr.Fields[0].Expected == "" {
		t.Fatalf("ValidationError = %+v, %v", verr, ok)
	}
	if _, ok := ValidationError(errors.New("other")); ok {
		t.Error("unrelated errors are not validation errors")
	}
}

func TestNewRejectsBadURLs(t *testing.T) {
	for _, u := range []string{"", "127.0.0.1:9000", "ftp://host", "http://", "not a url"} {
		if _, err := New(u, "t"); err == nil {
			t.Errorf("New(%q): expected an error", u)
		}
	}
}

func TestSendsTokenInCleartext(t *testing.T) {
	tests := map[string]bool{
		"http://127.0.0.1:9000":     false,
		"http://localhost:9000":     false,
		"http://[::1]:9000":         false,
		"https://agent.example.com": false,
		"http://agent.example.com":  true,
		"http://10.0.0.5:9000":      true, // a private network is still a network
	}
	for u, want := range tests {
		c, err := New(u, "token")
		if err != nil {
			t.Fatal(err)
		}
		if got := c.SendsTokenInCleartext(); got != want {
			t.Errorf("%s: got %v, want %v", u, got, want)
		}
	}
	if c, _ := New("http://agent.example.com", ""); c.SendsTokenInCleartext() {
		t.Error("without a token there is nothing to leak")
	}
}
