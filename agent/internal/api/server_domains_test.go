package api

import (
	"net/http"
	"strings"
	"testing"

	"github.com/shipwick/shipwick/pkg/api"
)

func TestHostnameConflictNamesTheField(t *testing.T) {
	f := newFixture(t)
	const first = "name: web\nimage: nginx:1.27\nport: 80\ndomain: web.example.com\nredirects: [www.example.com]\n"
	if status, body := f.do("POST", "/api/v1/applications/web/deploy", first); status != http.StatusAccepted {
		t.Fatalf("first deploy: %d %s", status, body)
	}
	f.engine.Wait()

	status, body := f.do("POST", "/api/v1/applications/other/deploy",
		"name: other\nimage: nginx:1.27\nport: 80\ndomain: other.example.com\naliases: [api.example.com, www.example.com]\n")
	e := decodeError(t, body)
	if status != http.StatusBadRequest || e.Code != api.CodeInvalidConfig {
		t.Fatalf("status = %d, error = %+v", status, e)
	}
	fields, _ := e.Details["fields"].([]any)
	field, _ := fields[0].(map[string]any)
	if len(fields) != 1 || field["field"] != "aliases[1]" || !strings.Contains(field["message"].(string), `application "web"`) {
		t.Errorf("details = %+v, want the offending entry named so the user knows which line to change", e.Details)
	}

	_, list := f.do("GET", "/api/v1/applications", "")
	if !strings.Contains(string(list), `"redirects":["www.example.com"]`) {
		t.Errorf("the list should carry the hostnames next to the domain: %s", list)
	}
}
