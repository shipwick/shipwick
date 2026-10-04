package docker

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/moby/moby/client"
)

// silentDaemon accepts every request and answers none until the test ends:
// what a daemon does that is stopped by a signal or waiting for its disk.
func silentDaemon(t *testing.T) *Runtime {
	t.Helper()
	release := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		select {
		case <-release:
		case <-r.Context().Done():
		}
	}))
	t.Cleanup(func() {
		close(release)
		srv.Close()
	})
	return runtimeAt(t, "tcp://"+srv.Listener.Addr().String())
}

func runtimeAt(t *testing.T, host string) *Runtime {
	t.Helper()
	cli, err := client.New(client.WithHost(host))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cli.Close() })
	return &Runtime{cli: cli, network: "shipwick", services: ServicesNetwork("shipwick"), rest: newAddressRest(0), answerWithin: 50 * time.Millisecond}
}

func TestADaemonThatTakesAQuestionAndNeverAnswersIsUnavailableAfterTheBound(t *testing.T) {
	r := silentDaemon(t)
	questions := map[string]func(context.Context) error{
		"listing containers": func(ctx context.Context) error { _, err := r.ListContainers(ctx, ""); return err },
		"inspecting one":     func(ctx context.Context) error { _, err := r.InspectContainer(ctx, "abc"); return err },
		"describing itself":  func(ctx context.Context) error { _, err := r.Info(ctx); return err },
	}
	for what, ask := range questions {
		err := ask(context.Background())
		if !errors.Is(err, ErrUnavailable) || !IsUnavailable(err) {
			t.Errorf("%s: err = %v, want ErrUnavailable", what, err)
			continue
		}
		if !strings.Contains(err.Error(), "Docker does not answer: no answer within 50ms") {
			t.Errorf("%s: err = %q; it must say how long was waited", what, err)
		}
	}
}

func TestACallerThatGivesUpFirstIsNotToldThatDockerIsUnavailable(t *testing.T) {
	r := silentDaemon(t)
	r.answerWithin = time.Minute
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := r.ListContainers(ctx, "")
	if err == nil || IsUnavailable(err) {
		t.Errorf("err = %v, want the caller's own cancellation", err)
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("err = %v, want it to wrap context.Canceled", err)
	}
}

func TestADaemonThatIsNotRunningIsUnavailableWhicheverCallFindsOut(t *testing.T) {
	// A port nothing listens on: taken, and given back.
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := l.Addr().String()
	l.Close()
	r := runtimeAt(t, "tcp://"+addr)
	r.answerWithin = time.Minute

	_, err = r.ListContainers(context.Background(), "")
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ListContainers: err = %v, want ErrUnavailable", err)
	}
	if !strings.Contains(err.Error(), addr) {
		t.Errorf("err = %q; it must keep what the client said", err)
	}
	// A call that is not bounded says so too, in the client's own type.
	if err := r.StartContainer(context.Background(), "abc"); !IsUnavailable(err) {
		t.Errorf("StartContainer: err = %v, want one IsUnavailable recognizes", err)
	}
}

func TestOtherErrorsAreNotTakenForAnOutage(t *testing.T) {
	for _, err := range []error{nil, ErrNotFound, fmt.Errorf("inspect container: %w", ErrNotFound), errors.New("no such image"), context.DeadlineExceeded} {
		if IsUnavailable(err) {
			t.Errorf("%v is taken for a daemon that does not answer", err)
		}
	}
}
