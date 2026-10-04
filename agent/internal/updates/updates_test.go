package updates

import (
	"context"
	"net/http"
	"net/http/httptest"
	"slices"
	"strings"
	"testing"
)

// github answers like the website's /releases/latest, and keeps the requests
// it was sent.
func github(t *testing.T, location string, status int) (*Client, *[]*http.Request) {
	t.Helper()
	var requests []*http.Request
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests = append(requests, r)
		if location != "" {
			w.Header().Set("Location", location)
		}
		w.WriteHeader(status)
	}))
	t.Cleanup(srv.Close)
	return newClient(srv.URL+"/shipwick/shipwick/releases/latest", http.DefaultTransport), &requests
}

func TestLatestReadsTheTagTheRedirectPointsAt(t *testing.T) {
	client, requests := github(t, "https://github.com/shipwick/shipwick/releases/tag/v0.7.1", http.StatusFound)
	tag, err := client.Latest(context.Background())
	if err != nil || tag != "v0.7.1" {
		t.Fatalf("Latest = %q, %v", tag, err)
	}
	if len(*requests) != 1 {
		t.Errorf("%d requests, want one: the redirect is the answer and is not followed", len(*requests))
	}
}

func TestTheRequestSaysNothingAboutTheServer(t *testing.T) {
	client, requests := github(t, "https://github.com/shipwick/shipwick/releases/tag/v0.7.1", http.StatusFound)
	if _, err := client.Latest(context.Background()); err != nil {
		t.Fatal(err)
	}
	r := (*requests)[0]
	if r.Method != http.MethodHead || r.URL.Path != "/shipwick/shipwick/releases/latest" || r.URL.RawQuery != "" || r.ContentLength > 0 {
		t.Errorf("request = %s %s (%d bytes), want a bare HEAD of the address", r.Method, r.URL, r.ContentLength)
	}
	// What net/http adds by itself, and the name of the program: no version,
	// no cookie, no identifier.
	var names []string
	for name := range r.Header {
		names = append(names, name)
	}
	slices.Sort(names)
	if got := strings.Join(names, ","); got != "User-Agent" {
		t.Errorf("headers = %s, want User-Agent alone", got)
	}
	if got := r.UserAgent(); got != "shipwick-agent" {
		t.Errorf("User-Agent = %q, want the program's name and nothing else", got)
	}
}

func TestLatestRefusesWhatIsNotARelease(t *testing.T) {
	cases := map[string]struct {
		location string
		status   int
		want     string
	}{
		"no redirect":           {"", http.StatusOK, "did not point at a release (HTTP 200)"},
		"a server error":        {"", http.StatusBadGateway, "did not point at a release (HTTP 502)"},
		"a redirect elsewhere":  {"https://github.com/login", http.StatusFound, "did not point at a release"},
		"a tag without version": {"https://github.com/shipwick/shipwick/releases/tag/nightly", http.StatusFound, `points at "nightly", which is not a release version`},
		"a pre-release":         {"https://github.com/shipwick/shipwick/releases/tag/v0.8.0-rc.1", http.StatusFound, "which is not a release version"},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			client, _ := github(t, tc.location, tc.status)
			tag, err := client.Latest(context.Background())
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("Latest = %q, %v; want an error with %q", tag, err, tc.want)
			}
		})
	}
}

func TestLatestGivesUpOnAServerThatCannotBeReached(t *testing.T) {
	client, _ := github(t, "", http.StatusOK)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := client.Latest(ctx); err == nil {
		t.Fatal("no error from a request that was not allowed to leave")
	}
}
