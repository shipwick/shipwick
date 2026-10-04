package upstreams

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/caddyserver/caddy/v2"
)

// asksAgain is a request for name that must not be served from what the
// table held: it returns the question the request waits for.
func (l *lab) asksAgain(name string) (<-chan result, question) {
	l.t.Helper()
	done := l.request(context.Background(), name)
	select {
	case r := <-done:
		l.t.Fatalf("served %v, %v without asking", r.addrs, r.err)
	case q := <-l.asked:
		if q.name != name {
			l.t.Fatalf("asked for %q, want %q", q.name, name)
		}
		return done, q
	}
	panic("unreachable")
}

// askedAgain is the question the table asks instead of believing the answer
// it was just given; done is the request that waits for it.
func (l *lab) askedAgain(done <-chan result, name string) question {
	l.t.Helper()
	select {
	case r := <-done:
		l.t.Fatalf("served %v, %v from an answer that was asked for before the drop", r.addrs, r.err)
	case q := <-l.asked:
		if q.name != name {
			l.t.Fatalf("asked for %q, want %q", q.name, name)
		}
		return q
	}
	panic("unreachable")
}

func TestADroppedNameIsAskedAgainAtOnce(t *testing.T) {
	l := newLab(t)
	l.answered("api_8080", "172.18.0.5", "172.18.0.7")
	l.answered("web_3000", "172.18.0.4")

	// The agent stopped the replica at .5; another application may get that
	// address now.
	l.table.drop([]string{"api", "api_8080"})

	done, q := l.asksAgain("api_8080")
	q.answer <- answer{addrs: []string{"172.18.0.7"}}
	l.want(done, "172.18.0.7")

	// Nobody dropped the other name: no question, which nobody would answer.
	l.want(l.request(context.Background(), "web_3000"), "172.18.0.4")
}

func TestAnAnswerAskedForBeforeTheDropIsNotBelieved(t *testing.T) {
	l := newLab(t)
	// The first lookup of the name is out when the replica is stopped: Docker
	// may have written the answer while the replica was still there.
	done := l.request(context.Background(), "api_8080")
	early := l.question("api_8080")
	l.table.drop([]string{"api_8080"})
	early.answer <- answer{addrs: []string{"172.18.0.5", "172.18.0.7"}}

	l.askedAgain(done, "api_8080").answer <- answer{addrs: []string{"172.18.0.7"}}
	l.want(done, "172.18.0.7")
	// And it is the second answer that was stored.
	l.want(l.request(context.Background(), "api_8080"), "172.18.0.7")
}

func TestARefreshThatWasOutDuringTheDropIsAskedAgain(t *testing.T) {
	l := newLab(t)
	l.answered("api_8080", "172.18.0.5", "172.18.0.7")
	l.pass(time.Second)

	done := l.request(context.Background(), "api_8080")
	early := l.question("api_8080")
	l.table.drop([]string{"api_8080"})
	// The request's patience ends: before the drop it would have gone to the
	// replicas of the last answer.
	l.outOfPatience()
	early.answer <- answer{addrs: []string{"172.18.0.5", "172.18.0.7"}}

	l.askedAgain(done, "api_8080").answer <- answer{addrs: []string{"172.18.0.7"}}
	l.want(done, "172.18.0.7")
}

func TestADroppedNameHasNoAnswerToKeepWhileDNSIsSilent(t *testing.T) {
	l := newLab(t)
	l.answered("api_8080", "172.18.0.5")
	l.pass(time.Second)

	// Docker's DNS goes silent, and the last answer is served meanwhile.
	silent := l.request(context.Background(), "api_8080")
	l.question("api_8080").answer <- answer{err: context.DeadlineExceeded}
	l.want(silent, "172.18.0.5")

	// Dropped, the name has nothing to serve: the answer that was kept names
	// a container that is gone.
	l.table.drop([]string{"api_8080"})
	done, q := l.asksAgain("api_8080")
	q.answer <- answer{err: context.DeadlineExceeded}
	if r := <-done; r.err == nil {
		t.Fatalf("served %v after the name was dropped and without an answer", r.addrs)
	}

	l.pass(time.Second)
	done, q = l.asksAgain("api_8080")
	q.answer <- answer{addrs: []string{"172.18.0.9"}}
	l.want(done, "172.18.0.9")
}

func TestDroppingANameNobodyAskedForIsNothing(t *testing.T) {
	l := newLab(t)
	l.table.drop([]string{"api_8080"})
	l.table.drop(nil)
	l.answered("api_8080", "172.18.0.5")
}

func TestTheAgentTellsTheProxyToForget(t *testing.T) {
	l := newLab(t)
	l.answered("api_8080", "172.18.0.5")
	forget := forgetHandler(l.table)

	post := func(body string) (int, error) {
		w := httptest.NewRecorder()
		err := forget(w, httptest.NewRequest(http.MethodPost, forgetPath, strings.NewReader(body)))
		return w.Code, err
	}
	status := func(err error) int {
		var apiErr caddy.APIError
		if !errors.As(err, &apiErr) {
			t.Fatalf("err = %v, want an APIError", err)
		}
		return apiErr.HTTPStatus
	}

	if _, err := post(`{"names": `); status(err) != http.StatusBadRequest {
		t.Errorf("half a body: %v", err)
	}
	w := httptest.NewRecorder()
	if err := forget(w, httptest.NewRequest(http.MethodGet, forgetPath, nil)); status(err) != http.StatusMethodNotAllowed {
		t.Errorf("GET: %v", err)
	}
	// Neither dropped anything.
	l.want(l.request(context.Background(), "api_8080"), "172.18.0.5")

	if code, err := post(`{"names": ["api", "api_8080"]}`); err != nil || code != http.StatusOK {
		t.Fatalf("forget: %d, %v", code, err)
	}
	done, q := l.asksAgain("api_8080")
	q.answer <- answer{addrs: []string{"172.18.0.6"}}
	l.want(done, "172.18.0.6")

	// No names is a question the agent asks too: does this proxy forget?
	if code, err := post(`{"names": []}`); err != nil || code != http.StatusOK {
		t.Errorf("no names: %d, %v", code, err)
	}
}

func TestTheEndpointIsPartOfTheAdminAPI(t *testing.T) {
	info, err := caddy.GetModule("admin.api.shipwick")
	if err != nil {
		t.Fatal(err)
	}
	router, ok := info.New().(caddy.AdminRouter)
	if !ok {
		t.Fatalf("%T adds no routes", info.New())
	}
	if routes := router.Routes(); len(routes) != 1 || routes[0].Pattern != "/shipwick/forget" {
		t.Errorf("routes = %+v", routes)
	}
}
