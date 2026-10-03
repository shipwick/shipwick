package dockertest

import (
	"context"
	"time"
)

// FollowOutput emits the lines given to WriteOutput for the container, the
// ones written before the call included, and stays open until the caller
// cancels or EndOutput closes the stream — a container that stopped.
func (f *Fake) FollowOutput(ctx context.Context, id string, since time.Time, emit func(line []byte)) error {
	f.mu.Lock()
	if f.FollowErr != nil {
		err := f.FollowErr
		f.mu.Unlock()
		return err
	}
	lines, ok := f.output[id]
	if !ok {
		lines = make(chan []byte, 1024)
		if f.output == nil {
			f.output = map[string]chan []byte{}
		}
		f.output[id] = lines
	}
	f.follows = append(f.follows, since)
	f.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return nil
		case line, open := <-lines:
			if !open {
				f.mu.Lock()
				if f.output[id] == lines {
					delete(f.output, id)
				}
				f.mu.Unlock()
				return nil
			}
			emit(line)
		}
	}
}

// WriteOutput makes the container print lines on its standard output.
func (f *Fake) WriteOutput(id string, lines ...string) {
	f.mu.Lock()
	ch, ok := f.output[id]
	if !ok {
		ch = make(chan []byte, 1024)
		if f.output == nil {
			f.output = map[string]chan []byte{}
		}
		f.output[id] = ch
	}
	f.mu.Unlock()
	for _, l := range lines {
		ch <- []byte(l)
	}
}

// EndOutput ends the container's output, as its stopping would: whoever
// follows it is handed what was written and then let go. The next
// FollowOutput after that starts a new stream.
func (f *Fake) EndOutput(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch, ok := f.output[id]; ok {
		close(ch)
	}
}

// Follows lists the `since` of every FollowOutput call so far, in order.
func (f *Fake) Follows() []time.Time {
	f.mu.Lock()
	defer f.mu.Unlock()
	return append([]time.Time(nil), f.follows...)
}
