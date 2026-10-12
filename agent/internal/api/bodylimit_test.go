package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
	"github.com/shipwick/shipwick/pkg/version"
)

// Every bounded body answers the same way over its limit: the status is
// 413, and the code the one the table of docs/api.md gives it.
func TestABodyOverItsLimitIs413WhateverTheEndpoint(t *testing.T) {
	f := newFixture(t)
	big := `{"image": "` + strings.Repeat("x", 8<<10) + `"}`
	for _, path := range []string{
		"/api/v1/applications/my-api/redeploy",
		"/api/v1/applications/my-api/rollback",
	} {
		status, body := f.do("POST", path, big)
		e := decodeError(t, body)
		if status != http.StatusRequestEntityTooLarge || e.Code != api.CodeInvalidRequest || !strings.Contains(e.Message, "exceeds 4 KB") {
			t.Errorf("%s with an 8 KB body: status %d, %+v; want 413 INVALID_REQUEST naming the limit", path, status, e)
		}
	}

	// A body within the limit that is not JSON is the client's mistake, 400.
	status, body := f.do("POST", "/api/v1/applications/my-api/redeploy", "{not json")
	if e := decodeError(t, body); status != http.StatusBadRequest || e.Code != api.CodeInvalidRequest {
		t.Errorf("a broken body: status %d, %+v; want 400 INVALID_REQUEST", status, e)
	}
}

// A CLI compares its version with the agent's through this field; the
// handler is the only place the agent says which version it is without a
// token.
func TestHealthCarriesTheAgentVersion(t *testing.T) {
	f := newFixture(t)
	status, body := f.doWithAuth("GET", "/api/v1/health", "", "")
	if status != http.StatusOK {
		t.Fatalf("status = %d", status)
	}
	if h := decode[api.Health](t, body); h.Version != version.Version {
		t.Errorf("version = %q, want the build's %q", h.Version, version.Version)
	}
}
