// Package notifytest provides a Notifier that remembers what it was told,
// for tests of the code that decides what is worth telling.
package notifytest

import (
	"context"
	"sync"

	"github.com/shipwick/shipwick/agent/internal/notify"
)

type Recorder struct {
	mu     sync.Mutex
	events []notify.Event
}

func (r *Recorder) Notify(_ context.Context, e notify.Event) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, e)
}

// Events returns everything recorded so far, oldest first.
func (r *Recorder) Events() []notify.Event {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]notify.Event(nil), r.events...)
}

// Kinds returns the kinds of the recorded events, oldest first.
func (r *Recorder) Kinds() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]string, len(r.events))
	for i, e := range r.events {
		out[i] = e.Kind
	}
	return out
}
