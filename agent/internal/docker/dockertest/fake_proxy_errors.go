package dockertest

import (
	"context"
	"time"
)

// FollowErrors is FollowOutput for the standard error: it emits the lines
// given to WriteErrors, the ones written before the call included, and stays
// open until the caller cancels or EndErrors closes the stream.
func (f *Fake) FollowErrors(ctx context.Context, id string, _ time.Time, emit func(line []byte)) error {
	f.mu.Lock()
	if f.FollowErr != nil {
		err := f.FollowErr
		f.mu.Unlock()
		return err
	}
	lines := f.errStream(id)
	f.mu.Unlock()

	for {
		select {
		case <-ctx.Done():
			return nil
		case line, open := <-lines:
			if !open {
				f.mu.Lock()
				if f.errOutput[id] == lines {
					delete(f.errOutput, id)
				}
				f.mu.Unlock()
				return nil
			}
			emit(line)
		}
	}
}

// errStream is the container's standard error, made when first asked for.
// The caller holds f.mu.
func (f *Fake) errStream(id string) chan []byte {
	ch, ok := f.errOutput[id]
	if !ok {
		ch = make(chan []byte, 1024)
		if f.errOutput == nil {
			f.errOutput = map[string]chan []byte{}
		}
		f.errOutput[id] = ch
	}
	return ch
}

// WriteErrors makes the container print lines on its standard error.
func (f *Fake) WriteErrors(id string, lines ...string) {
	f.mu.Lock()
	ch := f.errStream(id)
	f.mu.Unlock()
	for _, l := range lines {
		ch <- []byte(l)
	}
}

// EndErrors ends the container's standard error, as its stopping would.
func (f *Fake) EndErrors(id string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if ch, ok := f.errOutput[id]; ok {
		close(ch)
	}
}
