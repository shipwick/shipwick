package docker

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"strconv"
	"time"

	"github.com/moby/moby/api/pkg/stdcopy"
	"github.com/moby/moby/client"
)

// FollowOutput streams what a container writes to its standard output from
// `since` on, and calls emit for every line until the container stops or ctx
// is cancelled; cancellation is not an error. The line is only valid during
// the call. Unlike FollowLogs it asks for no timestamps and no standard
// error: the reader is a program, the lines carry their own time, and at a
// few thousand lines a second every allocation saved per line counts.
func (r *Runtime) FollowOutput(ctx context.Context, id string, since time.Time, emit func(line []byte)) error {
	rc, err := r.cli.ContainerLogs(ctx, id, client.ContainerLogsOptions{
		ShowStdout: true,
		Follow:     true,
		Since:      sinceParam(since),
	})
	if err != nil {
		return fmt.Errorf("follow output: %w", wrapNotFound(err))
	}
	defer rc.Close()

	out := &rawLineWriter{emit: emit}
	_, err = stdcopy.StdCopy(out, io.Discard, rc)
	if err != nil && ctx.Err() == nil {
		return fmt.Errorf("follow output: %w", err)
	}
	return nil
}

// sinceParam is a time as the Engine API takes it: seconds, with nanoseconds
// after the point.
func sinceParam(t time.Time) string {
	return strconv.FormatInt(t.Unix(), 10) + "." + fmt.Sprintf("%09d", t.Nanosecond())
}

// rawLineWriter reassembles a byte stream into lines without copying them.
type rawLineWriter struct {
	emit func([]byte)
	buf  []byte
}

func (w *rawLineWriter) Write(p []byte) (int, error) {
	n := len(p)
	if len(w.buf) > 0 {
		// The rest of a line whose beginning came with the previous write.
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			w.buf = append(w.buf, p...)
			if len(w.buf) > maxLogLineBytes {
				w.buf = w.buf[:0] // not a line anybody can use
			}
			return n, nil
		}
		w.buf = append(w.buf, p[:i]...)
		w.emit(w.buf)
		w.buf = w.buf[:0]
		p = p[i+1:]
	}
	for {
		i := bytes.IndexByte(p, '\n')
		if i < 0 {
			break
		}
		w.emit(p[:i])
		p = p[i+1:]
	}
	w.buf = append(w.buf, p...)
	return n, nil
}
