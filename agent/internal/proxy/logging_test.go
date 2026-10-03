package proxy

import (
	"encoding/json"
	"reflect"
	"sort"
	"strings"
	"testing"
)

// logged mirrors the parts of Caddy's config that decide what is logged and
// what is compressed.
type logged struct {
	Logging struct {
		Logs map[string]struct {
			Include []string
			Exclude []string
			Writer  struct{ Output string }
			Encoder struct {
				Format string
				Wrap   struct{ Format string }
				Fields map[string]struct {
					Filter string
					Regexp string
					Value  *string
				}
			}
		}
	}
	Apps struct {
		HTTP struct {
			Servers map[string]struct {
				Logs struct {
					DefaultLoggerName string   `json:"default_logger_name"`
					SkipHosts         []string `json:"skip_hosts"`
				}
				Routes []struct {
					Match  []struct{ Host []string }
					Handle []json.RawMessage
				}
			}
		}
	}
}

func buildLogged(t *testing.T, routes []Route) logged {
	t.Helper()
	_, _, raw := build(t, routes)
	var l logged
	if err := json.Unmarshal(raw, &l); err != nil {
		t.Fatal(err)
	}
	return l
}

func TestAccessLogGoesToStandardOutputAndStaysOutOfTheDefaultLog(t *testing.T) {
	l := buildLogged(t, []Route{{Domain: "web.example.com", Backends: []Backend{{Name: "web_8080", Port: 8080}}}})

	if name := l.Apps.HTTP.Servers["shipwick"].Logs.DefaultLoggerName; name != "shipwick" {
		t.Fatalf("the server's access logger is %q, want shipwick: without one Caddy logs no requests", name)
	}
	access, ok := l.Logging.Logs["shipwick-access"]
	if !ok {
		t.Fatalf("logs = %v, want one for the access log", l.Logging.Logs)
	}
	if access.Writer.Output != "stdout" || !reflect.DeepEqual(access.Include, []string{"http.log.access.shipwick"}) {
		t.Errorf("access log = %+v, want only http.log.access.shipwick, on standard output", access)
	}
	if access.Encoder.Format != "filter" || access.Encoder.Wrap.Format != "json" {
		t.Errorf("encoder = %+v, want JSON behind the field filter", access.Encoder)
	}
	// Everything else Caddy says stays where it was, without the requests.
	if def := l.Logging.Logs["default"]; !reflect.DeepEqual(def.Exclude, []string{"http.log.access.shipwick"}) || def.Writer.Output != "" {
		t.Errorf("default log = %+v, want it to exclude the access log and keep its writer", def)
	}
}

func TestAccessLogCarriesNoHeadersAndNoQueryString(t *testing.T) {
	l := buildLogged(t, nil)
	fields := l.Logging.Logs["shipwick-access"].Encoder.Fields

	var removed []string
	for name, f := range fields {
		if f.Filter == "delete" {
			removed = append(removed, name)
		}
	}
	sort.Strings(removed)
	want := []string{"bytes_read", "request>headers", "request>proto", "request>remote_ip", "request>remote_port", "request>tls", "resp_headers", "user_id"}
	if !reflect.DeepEqual(removed, want) {
		t.Errorf("removed fields = %v\nwant            %v", removed, want)
	}
	// What is left: ts, request>host, >method, >uri, >client_ip, status,
	// duration, size. The URI loses everything from the question mark on.
	uri := fields["request>uri"]
	if uri.Filter != "regexp" || uri.Regexp != `\?.*$` || uri.Value == nil || *uri.Value != "" {
		t.Errorf("request>uri = %+v, want the query string cut off: it carries tokens", uri)
	}
}

func TestRequestsForShipwickItselfAreNotLogged(t *testing.T) {
	l := buildLogged(t, []Route{
		{Domain: "web.example.com", Aliases: []string{"web2.example.com"}, Backends: []Backend{{Name: "web_8080", Port: 8080}}},
		{Domain: "agent.example.com", Upstreams: []string{"agent:9000"}, Streaming: true},
		{Domain: "dash.example.com", Upstreams: []string{"dashboard:3000"}, Streaming: true},
		{Domain: "site.example.com", StaticRoot: "/srv/shipwick/site/abc"},
	})
	skip := l.Apps.HTTP.Servers["shipwick"].Logs.SkipHosts
	if !reflect.DeepEqual(skip, []string{"agent.example.com", "dash.example.com"}) {
		t.Errorf("skip_hosts = %v, want the agent's and the dashboard's hostnames and no application's", skip)
	}

	if l := buildLogged(t, []Route{{Domain: "web.example.com"}}); l.Apps.HTTP.Servers["shipwick"].Logs.SkipHosts != nil {
		t.Errorf("skip_hosts = %v without a hostname to skip", l.Apps.HTTP.Servers["shipwick"].Logs.SkipHosts)
	}
}

func TestStreamingRouteIsCompressedExceptForAFollowedLog(t *testing.T) {
	l := buildLogged(t, []Route{{Domain: "agent.example.com", Upstreams: []string{"agent:9000"}, Streaming: true}})
	handle := l.Apps.HTTP.Servers["shipwick"].Routes[0].Handle
	if len(handle) != 2 {
		t.Fatalf("a streaming route has %d handlers, want the encoder's subroute and the proxy", len(handle))
	}
	var sub struct {
		Handler string
		Routes  []struct {
			Match []struct {
				Not []struct {
					Query map[string][]string
				}
			}
			Handle []struct {
				Handler       string
				Encodings     map[string]any
				MinimumLength int `json:"minimum_length"`
			}
		}
	}
	if err := json.Unmarshal(handle[0], &sub); err != nil {
		t.Fatal(err)
	}
	if sub.Handler != "subroute" || len(sub.Routes) != 1 || len(sub.Routes[0].Match) != 1 || len(sub.Routes[0].Match[0].Not) != 1 {
		t.Fatalf("first handler = %s, want a subroute with one route matched by a negation", handle[0])
	}
	// The encoder keeps the response header back until the body starts, and
	// a followed log of a quiet application has no body for minutes.
	if q := sub.Routes[0].Match[0].Not[0].Query; !reflect.DeepEqual(q, map[string][]string{"follow": {"*"}}) {
		t.Errorf("the encoder is skipped for %v, want for any request with a follow parameter", q)
	}
	enc := sub.Routes[0].Handle
	if len(enc) != 1 || enc[0].Handler != "encode" || len(enc[0].Encodings) != 2 || enc[0].MinimumLength != 1024 {
		t.Errorf("inside the subroute: %+v, want the same encoder application routes get", enc)
	}
	if !strings.Contains(string(handle[1]), `"reverse_proxy"`) || !strings.Contains(string(handle[1]), `"flush_interval":-1`) {
		t.Errorf("second handler = %s, want the unbuffered proxy", handle[1])
	}

	// Application routes get the encoder as it is.
	l = buildLogged(t, []Route{{Domain: "web.example.com", Backends: []Backend{{Name: "web_8080", Port: 8080}}}})
	if first := string(l.Apps.HTTP.Servers["shipwick"].Routes[0].Handle[0]); !strings.Contains(first, `"handler":"encode"`) {
		t.Errorf("an application route starts with %s, want the encoder", first)
	}
}
