package proxy

import (
	"encoding/json"
	"testing"
)

// fileServerRoute mirrors the file_server handler's fields.
type fileServerRoute struct {
	Match  []struct{ Host []string }
	Handle []struct {
		Handler    string
		Root       string
		IndexNames []string `json:"index_names"`
		StatusCode int      `json:"status_code"`
	}
}

func TestBuildStaticRoute(t *testing.T) {
	raw, fp, err := Build("unix//run/caddy/admin.sock", []Route{{
		Domain:     "example.com",
		Aliases:    []string{"web.example.com"},
		Redirects:  []string{"www.example.com"},
		StaticRoot: "/srv/shipwick/web/0123abcd",
	}})
	if err != nil {
		t.Fatalf("Build: %v", err)
	}
	var p struct {
		Apps struct {
			HTTP struct {
				Servers map[string]struct{ Routes []fileServerRoute }
			}
		}
	}
	if err := json.Unmarshal(raw, &p); err != nil {
		t.Fatalf("config is not valid JSON: %v\n%s", err, raw)
	}
	routes := p.Apps.HTTP.Servers["shipwick"].Routes
	if len(routes) != 3 {
		t.Fatalf("got %d routes, want the files, the redirect and the catch-all", len(routes))
	}
	files := routes[0]
	if len(files.Match[0].Host) != 2 || files.Match[0].Host[0] != "example.com" || files.Match[0].Host[1] != "web.example.com" {
		t.Errorf("hosts = %v; aliases are served like the domain", files.Match[0].Host)
	}
	// Files are compressed like application responses; the encoder comes
	// first, the file server last.
	if len(files.Handle) != 2 || files.Handle[0].Handler != "encode" {
		t.Fatalf("handlers = %+v; want encode then file_server", files.Handle)
	}
	h := files.Handle[1]
	if h.Handler != "file_server" || h.Root != "/srv/shipwick/web/0123abcd" || len(h.IndexNames) != 1 || h.IndexNames[0] != "index.html" {
		t.Errorf("handler = %+v; want a file_server at the root with index.html", h)
	}
	if redirect := routes[1].Handle[0]; redirect.Handler != "static_response" || redirect.StatusCode != 308 {
		t.Errorf("redirects work as for any application: %+v", redirect)
	}

	_, fpOther, _ := Build("unix//run/caddy/admin.sock", []Route{{Domain: "example.com", StaticRoot: "/srv/shipwick/web/4567ef01"}})
	if fpOther == fp {
		t.Error("another folder is another config: the proxy must reload for it")
	}
}
