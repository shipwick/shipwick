package deploy

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/shipwick/shipwick/agent/internal/store"
	"github.com/shipwick/shipwick/pkg/api"
)

// A promotion is begun by a request and belongs to nobody afterwards: it runs
// in the background, its record says how far it is, and whoever asked for it
// reads that record until it is done. The record is in the store, so the
// promotion also outlives the agent that began it. A server that was told to
// take over and then started half of its applications is the one state
// nobody asked for; an agent that finds a promotion unfinished goes on with
// it (recoverTransfers).
//
// Nothing more than the record is needed for that. An application the
// promotion had finished with keeps what the record says of it. One it had
// not reached is started. One it was at is looked at: recorded as running,
// it was started and is waited for again; recorded as stopped, it is started,
// which does no harm to a container that was started a moment before the
// agent stopped.

// ErrPromotionInProgress means the server is being promoted; one promotion
// runs at a time, and no import runs next to it.
var ErrPromotionInProgress = errors.New("a promotion is running on this server; follow it with: shipwick standby promote")

// promotionRecord is a promotion as the store keeps it: what the API shows,
// and who asked for it, for the events of the applications an agent starts
// after a restart.
type promotionRecord struct {
	api.Promotion
	Actor string `json:"actor"`
}

func copyPromotion(p api.Promotion) api.Promotion {
	p.Applications = append([]api.PromotedApplication{}, p.Applications...)
	p.Records = append([]api.DNSRecord{}, p.Records...)
	return p
}

// Promotion returns the promotion that is running, or ran last; ok is false
// when this server was never promoted.
func (e *Engine) Promotion() (api.Promotion, bool) {
	t := e.transfer
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.promotion == nil {
		return api.Promotion{}, false
	}
	return copyPromotion(t.promotion.Promotion), true
}

// StartPromotion begins starting every application that was imported stopped,
// in the order an import deploys them, and answers with the promotion's record
// as it begins: every application pending, and the DNS records to change.
// Promotion follows it.
//
// With nothing to start there is no promotion: the answer is one that has
// completed, with no applications and the id 0, and the record of the last
// real one stays.
func (e *Engine) StartPromotion(ctx context.Context) (api.Promotion, error) {
	t := e.transfer
	t.mu.Lock()
	defer t.mu.Unlock()
	switch {
	case t.promoting:
		return api.Promotion{}, ErrPromotionInProgress
	case t.importing:
		// Half of an export is not what a server takes over with.
		return api.Promotion{}, ErrImportInProgress
	}
	waiting, err := e.standbyApplications(ctx)
	if err != nil {
		return api.Promotion{}, err
	}
	now := time.Now().UTC()
	if len(waiting) == 0 {
		return api.Promotion{Applications: []api.PromotedApplication{}, Records: []api.DNSRecord{},
			Status: api.PromotionSucceeded, StartedAt: now, CompletedAt: &now}, nil
	}
	if !e.beginOp() {
		return api.Promotion{}, ErrShuttingDown
	}

	rec := &promotionRecord{Actor: actorFrom(ctx), Promotion: api.Promotion{
		ID: 1, Status: api.PromotionRunning, StartedAt: now,
		Applications: make([]api.PromotedApplication, 0, len(waiting)), Records: e.dnsRecords(waiting),
	}}
	if t.promotion != nil {
		rec.ID = t.promotion.ID + 1
	}
	for _, d := range waiting {
		rec.Applications = append(rec.Applications, api.PromotedApplication{Name: d.Application, Status: api.PromotedPending})
	}
	t.promoting, t.promotion, t.promoted = true, rec, make(chan struct{})
	e.keep(store.TransferPromotion, rec)
	e.log.Info("standby promoted", "by", rec.Actor, "promotion", rec.ID, "applications", len(waiting))
	go e.promote(rec)
	return copyPromotion(rec.Promotion), nil
}

// promote carries a promotion to its end, from wherever its record says it
// is. An application is waited for until it is ready, or until its startup
// budget is spent: what others reach by name comes first, and should be there
// when they start.
//
// An application that does not come up does not stop the promotion. The
// supervisor has it from the moment it is started, and the record says so.
//
// Only this function changes the record, under the transfer's mutex; what it
// reads of it, it reads as the only writer.
func (e *Engine) promote(rec *promotionRecord) {
	defer e.opDone()
	t := e.transfer
	ctx := WithActor(e.baseCtx, rec.Actor)
	set := func(i int, status, message string) {
		t.mu.Lock()
		defer t.mu.Unlock()
		rec.Applications[i].Status, rec.Applications[i].Message = status, message
		e.keep(store.TransferPromotion, rec)
	}

	for i := range rec.Applications {
		a := rec.Applications[i]
		if a.Status != api.PromotedPending && a.Status != api.PromotedStarting {
			continue
		}
		set(i, api.PromotedStarting, "")
		status, message := e.promoteApplication(ctx, a.Name)
		if e.baseCtx.Err() != nil {
			// Not an outcome: the agent is stopping. The record stays as it
			// is, and the agent that starts next goes on from it.
			e.log.Info("promotion interrupted by the agent shutting down; it resumes at the next start", "promotion", rec.ID, "at", a.Name)
			t.mu.Lock()
			close(t.promoted)
			t.mu.Unlock()
			return
		}
		set(i, status, message)
	}

	t.mu.Lock()
	now := time.Now().UTC()
	rec.CompletedAt, rec.Status = &now, api.PromotionSucceeded
	for _, a := range rec.Applications {
		if a.Status == api.PromotedFailed {
			rec.Status = api.PromotionFailed
		}
	}
	e.keep(store.TransferPromotion, rec)
	t.promoting = false
	close(t.promoted)
	t.mu.Unlock()
	e.log.Info("promotion finished", "promotion", rec.ID, "status", rec.Status)
}

// promoteApplication starts one application of a promotion, unless it runs
// already, and waits for it to be ready.
func (e *Engine) promoteApplication(ctx context.Context, name string) (status, message string) {
	app, d, err := e.activeDeployment(ctx, name)
	switch {
	case errors.Is(err, store.ErrNotFound), errors.Is(err, ErrNotDeployed):
		return api.PromotedFailed, "it is no longer deployed on this server"
	case err != nil:
		return api.PromotedFailed, err.Error()
	}
	if app.DesiredState != api.DesiredRunning {
		if err := e.Start(ctx, name); err != nil {
			if errors.Is(err, ErrBusy) {
				return api.PromotedFailed, "another operation is in progress for it; start it with: shipwick start " + name
			}
			return api.PromotedFailed, err.Error()
		}
	}
	if err := e.awaitStarted(ctx, d); err != nil {
		return api.PromotedStarted, fmt.Sprintf("started, and not ready yet: %v. It is restarted until it is; watch it with: shipwick status %s", err, name)
	}
	return api.PromotedRunning, ""
}

// AwaitPromotion returns the promotion once the one that runs has ended. It
// is for a caller that cannot follow a record; the promotion does not depend
// on anybody waiting.
func (e *Engine) AwaitPromotion(ctx context.Context) (api.Promotion, error) {
	t := e.transfer
	t.mu.Lock()
	done := t.promoted
	t.mu.Unlock()
	if done != nil {
		select {
		case <-done:
		case <-ctx.Done():
			return api.Promotion{}, ctx.Err()
		}
	}
	p, _ := e.Promotion()
	if p.CompletedAt == nil {
		return p, ErrShuttingDown
	}
	return p, nil
}

// Promote is StartPromotion and AwaitPromotion in one call.
func (e *Engine) Promote(ctx context.Context) (api.Promotion, error) {
	p, err := e.StartPromotion(ctx)
	if err != nil || p.CompletedAt != nil {
		return p, err
	}
	return e.AwaitPromotion(ctx)
}

// recoverTransfers reads back what the agent before this one recorded about
// imports and a promotion, and settles what it left unfinished. An import
// ends with the request or the download that fed it, so one found running
// has failed; a promotion needs nothing but its record, and goes on. Recover
// calls it, once.
func (e *Engine) recoverTransfers(ctx context.Context) error {
	t := e.transfer
	t.mu.Lock()
	defer t.mu.Unlock()

	var last api.Import
	if found, err := e.store.TransferState(ctx, store.TransferImport, &last); err != nil {
		return err
	} else if found {
		if last.Status == api.ImportRunning {
			// An agent that shuts down writes the import's end itself; this
			// one was not given the time.
			now := time.Now().UTC()
			last.Status, last.CompletedAt = api.ImportFailed, &now
			last.Error = "the agent restarted while the import ran. Run it again with --overwrite; what it had finished is in place"
			for i, a := range last.Applications {
				if a.Status == api.ImportAppPending || a.Status == api.ImportAppRunning {
					last.Applications[i].Status, last.Applications[i].Message = api.ImportAppFailed, "the import ended before it was done with it"
				}
			}
			e.keep(store.TransferImport, last)
		}
		t.last = copyImport(last)
	}

	if _, err := e.store.TransferState(ctx, store.TransferPull, &t.pull); err != nil {
		return err
	}

	var rec promotionRecord
	if found, err := e.store.TransferState(ctx, store.TransferPromotion, &rec); err != nil {
		return err
	} else if found {
		rec.Promotion = copyPromotion(rec.Promotion)
		t.promotion = &rec
		if rec.CompletedAt == nil {
			if !e.beginOp() {
				return ErrShuttingDown
			}
			t.promoting, t.promoted = true, make(chan struct{})
			e.log.Info("resuming promotion", "promotion", rec.ID)
			go e.promote(&rec)
		}
	}
	return nil
}
