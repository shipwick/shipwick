package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"slices"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestMissingLayersAsksWithTheImagesDiffIDs(t *testing.T) {
	var got *http.Request
	var body string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		fmt.Fprint(w, `{"data": {"missing": ["sha256:bb"]}}`)
	})
	missing, err := c.MissingLayers(context.Background(), "my-api", []string{"sha256:aa", "sha256:bb"})
	if err != nil || !slices.Equal(missing, []string{"sha256:bb"}) {
		t.Fatalf("missing = %v, %v", missing, err)
	}
	if got.Method != http.MethodPost || got.URL.Path != "/api/v1/applications/my-api/images/missing" {
		t.Errorf("unexpected request: %s %s", got.Method, got.URL)
	}
	if got.Header.Get("Content-Type") != "application/json" || body != `{"layers":["sha256:aa","sha256:bb"]}` {
		t.Errorf("Content-Type %q, body %s", got.Header.Get("Content-Type"), body)
	}
}

func TestMissingLayersReportsAnAgentThatDoesNotKnowTheQuestion(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprint(w, `{"error": {"code": "ENDPOINT_NOT_FOUND", "message": "no such endpoint: POST /api/v1/applications/my-api/images/missing", "details": {}}}`)
	})
	if _, err := c.MissingLayers(context.Background(), "my-api", []string{"sha256:aa"}); !IsCode(err, api.CodeEndpointNotFound) {
		t.Errorf("err = %v", err)
	}
}
