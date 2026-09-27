package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestPushImageStreamsATarBody(t *testing.T) {
	var got *http.Request
	var body string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		got = r.Clone(context.Background())
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		w.WriteHeader(http.StatusCreated)
		fmt.Fprint(w, `{"data": {"image": "shipwick.local/my-api:20260927-153000-a1b2", "size_bytes": 11}}`)
	})
	loaded, err := c.PushImage(context.Background(), "my-api", strings.NewReader("the archive"), 0)
	if err != nil {
		t.Fatalf("PushImage: %v", err)
	}
	if loaded.Image != "shipwick.local/my-api:20260927-153000-a1b2" || loaded.SizeBytes != 11 {
		t.Errorf("loaded = %+v", loaded)
	}
	if got.URL.Path != "/api/v1/applications/my-api/images" || got.Method != http.MethodPost {
		t.Errorf("unexpected request: %s %s", got.Method, got.URL)
	}
	if got.Header.Get("Content-Type") != "application/x-tar" || got.Header.Get("Authorization") != "Bearer secret-token" {
		t.Errorf("headers = %v", got.Header)
	}
	if got.ContentLength != -1 || body != "the archive" {
		t.Errorf("an archive of unknown size is sent chunked: length %d, body %q", got.ContentLength, body)
	}
}

func TestPushImageReportsTheAgentsRefusal(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error": {"code": "INVALID_REQUEST", "message": "the archive holds no tagged image", "details": {}}}`)
	})
	_, err := c.PushImage(context.Background(), "my-api", strings.NewReader("x"), 1)
	if !IsCode(err, api.CodeInvalidRequest) || err.Error() != "the archive holds no tagged image" {
		t.Errorf("err = %v", err)
	}
}
