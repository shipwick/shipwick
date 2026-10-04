package docker

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/moby/moby/client"
)

// ErrUnavailable means the Docker daemon did not answer: it is not running,
// or it took the question and said nothing for AnswerTimeout.
var ErrUnavailable = errors.New("Docker does not answer")

// AnswerTimeout bounds the questions that a daemon in working order answers
// in milliseconds: listing containers, inspecting one, describing itself. A
// daemon that is stopped by a signal, out of memory or waiting for a disk
// accepts the connection and never answers; without a bound, whoever asked —
// the supervisor's tick with an application's lock in its hand, a request for
// `shipwick status` — waits with it, and says nothing. Fifteen seconds is far
// beyond a slow answer and short of the thirty a user operation waits for the
// supervisor.
//
// Calls that take as long as their work — pulling, stopping, waiting for a
// container, following a log — are not bounded here: their callers bound them.
const AnswerTimeout = 15 * time.Second

// errNoAnswer is the cause the bound cancels with, which tells it apart from
// the caller's own deadline.
var errNoAnswer = errors.New("no answer")

func (r *Runtime) patience() time.Duration {
	if r.answerWithin > 0 {
		return r.answerWithin
	}
	return AnswerTimeout
}

// patient is the context of one bounded question.
func (r *Runtime) patient(ctx context.Context) (context.Context, context.CancelFunc) {
	return context.WithTimeoutCause(ctx, r.patience(), errNoAnswer)
}

// unanswered turns the failure of a bounded question into ErrUnavailable when
// it is the daemon's: the bound ran out, or nothing listens on the socket.
// ctx is the one patient returned. Every other error is returned as it is.
func (r *Runtime) unanswered(ctx context.Context, err error) error {
	switch {
	case err == nil:
		return nil
	case errors.Is(context.Cause(ctx), errNoAnswer):
		return fmt.Errorf("%w: no answer within %s", ErrUnavailable, r.patience())
	case client.IsErrConnectionFailed(err):
		return fmt.Errorf("%w: %v", ErrUnavailable, err)
	}
	return err
}

// IsUnavailable reports whether err says that the daemon did not answer,
// whichever call found out.
func IsUnavailable(err error) bool {
	return errors.Is(err, ErrUnavailable) || client.IsErrConnectionFailed(err)
}
