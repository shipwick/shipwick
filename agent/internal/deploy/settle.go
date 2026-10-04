package deploy

import (
	"context"
	"errors"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// A deployment ends with two writes: the status it settled in, and
// completed_at. Clients wait for the second one, and the agent that starts
// next takes a record without it for a deployment to go on with. A database
// that refuses writes at that moment — its disk is full — must therefore not
// have the last word: an end that could not be written is kept in memory and
// written as soon as the database takes it, on the supervisor's tick.
//
// The application's lock is not held meanwhile. The engine is done with the
// deployment, the supervisor has replicas to look after, and whatever the
// operator does next either fails for the same reason or shows that the
// database is writable again.

// unsettled is a deployment the engine is done with and the database does not
// say so yet.
type unsettled struct {
	d       store.Deployment // Status is what the database still holds
	failure string           // why it failed, if that could not be written either
}

// endNotRecorded is the error of a deployment that ended in a status of work
// without a failure of its own to name: every write of its last steps was
// refused.
const endNotRecorded = "the deployment ended, and the database could not be written at the time to say how"

// settle records the end of a deployment the engine is done with, or keeps it
// for retrySettling.
func (e *Engine) settle(r *rollout) {
	ctx, cancel := context.WithTimeout(context.Background(), cleanupTimeout)
	defer cancel()
	u := unsettled{d: *r.d, failure: r.failure}
	err := e.writeEnd(ctx, &u)
	if err == nil {
		return
	}
	e.log.Error("could not record the end of a deployment; it is written as soon as the database takes it",
		"app", u.d.Application, "deployment", u.d.ID, "status", u.d.Status, "error", err)
	e.mu.Lock()
	e.unsettled = append(e.unsettled, u)
	e.mu.Unlock()
}

// writeEnd writes what is missing of a deployment's end. Both writes can be
// repeated: the transition is a compare-and-swap from the status the database
// holds, and the completion time is stamped once.
func (e *Engine) writeEnd(ctx context.Context, u *unsettled) error {
	if !IsSettled(u.d.Status) {
		msg := u.failure
		if msg == "" {
			msg = endNotRecorded
		}
		err := e.transitionWithError(ctx, &u.d, api.StatusFailed, msg)
		if errors.Is(err, store.ErrConflict) {
			// A write that was reported as refused and was not: the database
			// knows better than this memory of it.
			current, rerr := e.store.GetDeployment(ctx, u.d.ID)
			if rerr != nil {
				return rerr
			}
			u.d.Status = current.Status
			if !IsSettled(u.d.Status) {
				return err
			}
		} else if err != nil {
			return err
		}
	}
	return e.store.CompleteDeployment(ctx, u.d.ID, time.Now())
}

// retrySettling writes the ends that could not be written before. It runs on
// every tick of the supervisor; a database that still refuses costs one
// failed statement per deployment, and says nothing: it was said when the end
// was kept.
func (e *Engine) retrySettling(ctx context.Context) {
	e.mu.Lock()
	pending := e.unsettled
	e.unsettled = nil
	e.mu.Unlock()
	if len(pending) == 0 {
		return
	}
	var still []unsettled
	for _, u := range pending {
		if err := e.writeEnd(ctx, &u); err != nil {
			still = append(still, u)
			continue
		}
		e.log.Info("recorded the end of a deployment that could not be written before",
			"app", u.d.Application, "deployment", u.d.ID, "status", u.d.Status)
	}
	if len(still) > 0 {
		e.mu.Lock()
		e.unsettled = append(still, e.unsettled...)
		e.mu.Unlock()
	}
}

// superseded reports whether the application has a deployment made after d.
// One lock per application means such a record can only have been created
// once the engine was done with d: d is in flight in the database alone, its
// end was never written, and going on with it would roll an old version out
// over whatever has been deployed since.
func (e *Engine) superseded(ctx context.Context, d store.Deployment) (bool, error) {
	latest, err := e.store.ListDeployments(ctx, store.DeploymentFilter{Application: d.Application, Limit: 1})
	if err != nil {
		return false, err
	}
	return len(latest) > 0 && latest[0].Sequence > d.Sequence, nil
}
