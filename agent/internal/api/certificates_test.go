package api

import (
	"encoding/json"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/shipwick/shipwick/agent/internal/certs/certstest"
	"github.com/shipwick/shipwick/pkg/api"
)

func certificateBody(t *testing.T, pair certstest.Pair) string {
	t.Helper()
	body, err := json.Marshal(api.SetCertificateRequest{Certificate: pair.Cert, Key: pair.Key})
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

// keyBody is the base64 of a pair's key without its PEM frame: what must
// never be seen outside the request.
func keyBody(pair certstest.Pair) string {
	lines := strings.Split(strings.TrimSpace(pair.Key), "\n")
	return lines[1]
}

func TestCertificateLifecycle(t *testing.T) {
	f := newFixture(t)
	notAfter := time.Now().AddDate(0, 3, 0)
	pair := certstest.Issue(notAfter, "example.com", "*.example.com")

	status, body := f.do("GET", "/api/v1/certificates", "")
	if status != http.StatusOK || strings.TrimSpace(string(body)) != `{"data":[]}` {
		t.Fatalf("empty list: status = %d, body = %s", status, body)
	}

	status, body = f.do("PUT", "/api/v1/certificates/Example.com", certificateBody(t, pair))
	if status != http.StatusOK {
		t.Fatalf("set: status = %d, body = %s", status, body)
	}
	stored := decode[api.Certificate](t, body)
	if stored.Hostname != "example.com" || strings.Join(stored.Subjects, ",") != "example.com,*.example.com" ||
		stored.Issuer != "Shipwick Test Authority" || stored.NotAfter.Unix() != notAfter.Unix() || stored.NotBefore.IsZero() ||
		stored.CreatedAt.IsZero() || stored.UpdatedAt.IsZero() {
		t.Errorf("stored = %+v", stored)
	}

	status, listBody := f.do("GET", "/api/v1/certificates", "")
	list := decode[[]api.Certificate](t, listBody)
	if status != http.StatusOK || len(list) != 1 || list[0].Hostname != "example.com" || !list[0].NotAfter.Equal(stored.NotAfter) {
		t.Errorf("list: status = %d, %+v", status, list)
	}
	var shape struct{ Data []map[string]any }
	if err := json.Unmarshal(listBody, &shape); err != nil {
		t.Fatal(err)
	}
	for _, field := range []string{"hostname", "subjects", "issuer", "not_before", "not_after", "created_at", "updated_at"} {
		if _, ok := shape.Data[0][field]; !ok {
			t.Errorf("the list lacks %q: %s", field, listBody)
		}
	}
	if len(shape.Data[0]) != 7 {
		t.Errorf("the list carries more than the seven documented fields: %s", listBody)
	}

	for what, text := range map[string]string{"the set response": string(body), "the list": string(listBody), "the log": f.logs.String()} {
		if strings.Contains(text, keyBody(pair)) || strings.Contains(text, "PRIVATE KEY") || strings.Contains(text, "BEGIN CERTIFICATE") {
			t.Errorf("%s carries the key or the PEM:\n%s", what, text)
		}
	}

	status, body = f.do("DELETE", "/api/v1/certificates/example.com", "")
	if status != http.StatusNoContent || len(body) != 0 {
		t.Fatalf("delete: status = %d, body = %s", status, body)
	}
	status, body = f.do("DELETE", "/api/v1/certificates/example.com", "")
	if e := decodeError(t, body); status != http.StatusNotFound || e.Code != api.CodeNotFound {
		t.Errorf("delete again: status = %d, error = %+v", status, e)
	}
}

func TestAWildcardCertificateIsStoredUnderTheWildcard(t *testing.T) {
	f := newFixture(t)
	pair := certstest.Issue(time.Now().AddDate(0, 3, 0), "*.example.com")
	status, body := f.do("PUT", "/api/v1/certificates/*.example.com", certificateBody(t, pair))
	if status != http.StatusOK || decode[api.Certificate](t, body).Hostname != "*.example.com" {
		t.Fatalf("status = %d, body = %s", status, body)
	}
	if status, _ := f.do("DELETE", "/api/v1/certificates/*.example.com", ""); status != http.StatusNoContent {
		t.Errorf("delete: status = %d", status)
	}
}

func TestCertificateRolesReadListsAdminWrites(t *testing.T) {
	f := newFixture(t)
	reader := "Bearer " + f.createToken("viewer", api.RoleRead).Token
	deployer := "Bearer " + f.createToken("ci", api.RoleDeploy).Token
	pair := certstest.Issue(time.Now().AddDate(0, 3, 0), "example.com")

	if status, _ := f.doWithAuth("GET", "/api/v1/certificates", "", reader); status != http.StatusOK {
		t.Errorf("read may list: status = %d", status)
	}
	for _, auth := range []string{reader, deployer} {
		for _, tt := range []struct{ method, path, body string }{
			{"PUT", "/api/v1/certificates/example.com", certificateBody(t, pair)},
			{"DELETE", "/api/v1/certificates/example.com", ""},
		} {
			status, body := f.doWithAuth(tt.method, tt.path, tt.body, auth)
			if e := decodeError(t, body); status != http.StatusForbidden || e.Code != api.CodeForbidden || e.Details["required"] != "admin" {
				t.Errorf("%s %s: status = %d, error = %+v", tt.method, tt.path, status, e)
			}
		}
	}
	if status, _ := f.doWithAuth("GET", "/api/v1/certificates", "", ""); status != http.StatusUnauthorized {
		t.Errorf("without a token: status = %d", status)
	}
}

func TestSetCertificateRefusesWithASentence(t *testing.T) {
	f := newFixture(t)
	pair := certstest.Issue(time.Now().AddDate(0, 3, 0), "example.com")
	other := certstest.Issue(time.Now().AddDate(0, 3, 0), "example.com")
	expired := certstest.Issue(time.Now().AddDate(0, 0, -1), "example.com")
	mismatched := certstest.Pair{Cert: pair.Cert, Key: other.Key}
	big := strings.Repeat("A", api.MaxCertificatePEMBytes+1)

	tests := []struct {
		name, path, body string
		code, want       string
	}{
		{"a hostname the certificate does not cover", "/api/v1/certificates/example.org", certificateBody(t, pair), api.CodeInvalidCertificate, "does not cover example.org"},
		{"a key of another certificate", "/api/v1/certificates/example.com", certificateBody(t, mismatched), api.CodeInvalidCertificate, "does not belong to the first certificate"},
		{"an expired certificate", "/api/v1/certificates/example.com", certificateBody(t, expired), api.CodeInvalidCertificate, "expired on"},
		{"a chain that is not PEM", "/api/v1/certificates/example.com", `{"certificate": "hello", "key": "world"}`, api.CodeInvalidCertificate, "the certificate is not PEM"},
		{"no key", "/api/v1/certificates/example.com", `{"certificate": "x"}`, api.CodeInvalidRequest, "key is required"},
		{"no certificate", "/api/v1/certificates/example.com", `{"key": "x"}`, api.CodeInvalidRequest, "certificate is required"},
		{"an unknown field", "/api/v1/certificates/example.com", `{"certificate": "x", "key": "y", "hostname": "z"}`, api.CodeInvalidRequest, "invalid JSON body"},
		{"not JSON", "/api/v1/certificates/example.com", `-----BEGIN CERTIFICATE-----`, api.CodeInvalidRequest, "invalid JSON body"},
		{"a chain over the limit", "/api/v1/certificates/example.com", `{"certificate": "` + big + `", "key": "y"}`, api.CodeInvalidRequest, "certificate is larger than 64 KB"},
		{"a hostname that is none", "/api/v1/certificates/not_a_hostname", certificateBody(t, pair), api.CodeInvalidRequest, "hostname:"},
		{"a wildcard in the middle", "/api/v1/certificates/a.*.example.com", certificateBody(t, pair), api.CodeInvalidRequest, "hostname:"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			status, body := f.do("PUT", tt.path, tt.body)
			e := decodeError(t, body)
			if status != http.StatusBadRequest || e.Code != tt.code || !strings.Contains(e.Message, tt.want) {
				t.Errorf("status = %d, error = %+v; want 400 %s containing %q", status, e, tt.code, tt.want)
			}
			if strings.Contains(string(body), keyBody(other)) || strings.Contains(string(body), keyBody(pair)) {
				t.Errorf("the error carries a key: %s", body)
			}
		})
	}
	if _, body := f.do("GET", "/api/v1/certificates", ""); strings.TrimSpace(string(body)) != `{"data":[]}` {
		t.Errorf("a refused certificate was stored: %s", body)
	}
	if logs := f.logs.String(); strings.Contains(logs, keyBody(pair)) || strings.Contains(logs, keyBody(other)) {
		t.Errorf("a key reached the log:\n%s", logs)
	}
}

func TestDeployingAWildcardWithoutACertificateIsAConfigError(t *testing.T) {
	f := newFixture(t)
	const config = "name: tenants\nimage: nginx:1.27\nport: 80\ndomain: \"*.example.com\"\n"

	status, body := f.do("POST", "/api/v1/applications/tenants/deploy", config)
	e := decodeError(t, body)
	if status != http.StatusBadRequest || e.Code != api.CodeInvalidConfig {
		t.Fatalf("status = %d, error = %+v; to the user this is a line of deploy.yaml to change", status, e)
	}
	fields, _ := e.Details["fields"].([]any)
	if len(fields) != 1 {
		t.Fatalf("details = %+v", e.Details)
	}
	field, _ := fields[0].(map[string]any)
	expected, _ := field["expected"].(string)
	if field["field"] != "domain" || !strings.Contains(expected, "SHIPWICK_CLOUDFLARE_API_TOKEN") || !strings.Contains(expected, "shipwick cert set '*.example.com'") {
		t.Errorf("field = %+v, want the domain field and what to do", field)
	}

	pair := certstest.Issue(time.Now().AddDate(0, 3, 0), "*.example.com")
	if status, body := f.do("PUT", "/api/v1/certificates/*.example.com", certificateBody(t, pair)); status != http.StatusOK {
		t.Fatalf("set certificate: status = %d, body = %s", status, body)
	}
	if status, body := f.do("POST", "/api/v1/applications/tenants/deploy", config); status != http.StatusAccepted {
		t.Errorf("with a certificate that covers it: status = %d, body = %s", status, body)
	}
}
