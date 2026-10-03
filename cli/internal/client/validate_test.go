package client

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestValidateSendsTheDocumentAndReportsAcceptance(t *testing.T) {
	var method, path, body string
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		method, path = r.Method, r.URL.Path
		b, _ := io.ReadAll(r.Body)
		body = string(b)
		fmt.Fprint(w, `{"data": {"valid": true}}`)
	})

	supported, err := c.Validate(context.Background(), "my-api", []byte("name: my-api\nbuild: .\n"))
	if err != nil || !supported {
		t.Fatalf("Validate: supported = %v, err = %v", supported, err)
	}
	if method != http.MethodPost || path != "/api/v1/applications/my-api/validate" || body != "name: my-api\nbuild: .\n" {
		t.Errorf("request = %s %s with body %q", method, path, body)
	}
}

func TestValidateReturnsTheRefusalADeploymentWouldGet(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusBadRequest)
		fmt.Fprint(w, `{"error": {"code": "INVALID_CONFIG", "message": "invalid deploy.yaml", "details": {"fields": [{"field": "domain", "message": "already served by application \"web\""}]}}}`)
	})

	supported, err := c.Validate(context.Background(), "my-api", []byte("name: my-api\n"))
	if !supported {
		t.Error("an agent that answers the question supports it, whatever the answer")
	}
	verr, ok := ValidationError(err)
	if !ok || len(verr.Fields) != 1 || verr.Fields[0].Field != "domain" {
		t.Errorf("err = %v, want the field-by-field report a deployment would get", err)
	}
}

func TestValidateIsNotAnErrorOnAnAgentThatPredatesIt(t *testing.T) {
	c := newTestClient(t, func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
		fmt.Fprintf(w, `{"error": {"code": %q, "message": "no such endpoint: POST /api/v1/applications/my-api/validate", "details": {}}}`, api.CodeEndpointNotFound)
	})

	supported, err := c.Validate(context.Background(), "my-api", []byte("name: my-api\n"))
	if supported || err != nil {
		t.Errorf("supported = %v, err = %v; an older agent cannot be asked, and that is all", supported, err)
	}
}
