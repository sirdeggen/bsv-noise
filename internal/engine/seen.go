package engine

import (
	"context"
	"errors"
	"sync"
	"time"

	"github.com/sirdeggen/bsv-noise/internal/arcadestatus"
	"github.com/sirdeggen/bsv-noise/internal/store"
)

// NetworkWatcher is the Arcade status source used to free MaxInFlight slots.
// Implementations tee the already-wired SSE stream and fall back to GET /tx/:txid.
type NetworkWatcher interface {
	NetworkEvents() <-chan arcadestatus.Event
	QueryNetworkStatus(ctx context.Context, txids []string) ([]arcadestatus.Event, error)
	Rebroadcast(ctx context.Context, rawHex string) error
}

// rebroadcastEvery is the minimum gap between rebroadcast attempts of one
// PENDING_RETRY transaction, so a parked Arcade host is retried without a
// tight loop.
const rebroadcastEvery = 2 * time.Minute

type rebroadcastTracker struct {
	mu   sync.Mutex
	last map[string]time.Time
}

func (e *Engine) seenLoop(ctx context.Context) error {
	if e.watch == nil || !e.rt.Broadcast {
		return nil
	}
	events := e.watch.NetworkEvents()
	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case ev, ok := <-events:
			if !ok {
				events = nil
				continue
			}
			e.applyNetworkEvent(ctx, ev)
		default:
			if err := e.pollNetworkStatus(ctx); err != nil {
				e.log.Warn("arcade status poll", "error", err.Error())
			}
			if err := e.clock.Sleep(ctx, e.rt.ConfirmEvery); err != nil {
				return err
			}
		}
	}
}

func (e *Engine) applyNetworkEvent(ctx context.Context, ev arcadestatus.Event) {
	if ev.TxID == "" || ev.Status == "" {
		return
	}
	now := e.clock.Now()
	if arcadestatus.IsRejected(ev.Status) {
		p, ok, err := e.st.LookupTxID(ev.TxID)
		if err != nil || !ok {
			return
		}
		if err := e.st.Fail(p.ID, "arcade "+ev.Status); err != nil {
			if !errors.Is(err, store.ErrConflict) {
				e.log.Warn("reject fail", "id", p.ID, "txid", ev.TxID, "status", ev.Status, "error", err.Error())
			}
			return
		}
		e.fail("arcade", nil)
		e.log.Info("payment rejected", "id", p.ID, "txid", ev.TxID, "status", ev.Status)
		return
	}
	if arcadestatus.IsPendingRetry(ev.Status) {
		e.maybeRebroadcast(ctx, ev)
		return
	}
	if !arcadestatus.FreesInFlight(ev.Status) {
		return
	}
	id, changed, err := e.st.MarkSeenByTxID(ev.TxID, now)
	if err != nil {
		if !errors.Is(err, store.ErrConflict) && !errors.Is(err, store.ErrNotFound) {
			e.log.Warn("mark seen", "txid", ev.TxID, "status", ev.Status, "error", err.Error())
		}
		return
	}
	if changed {
		e.log.Info("payment seen on network", "id", id, "txid", ev.TxID, "status", ev.Status)
	}
}

func (e *Engine) pollNetworkStatus(ctx context.Context) error {
	if e.watch == nil {
		return nil
	}
	pending, err := e.st.UnseenBroadcast(50)
	if err != nil {
		return err
	}
	if len(pending) == 0 {
		return nil
	}
	txids := make([]string, 0, len(pending))
	for _, p := range pending {
		txids = append(txids, p.TxID)
	}
	events, err := e.watch.QueryNetworkStatus(ctx, txids)
	if err != nil {
		return err
	}
	for _, ev := range events {
		e.applyNetworkEvent(ctx, ev)
	}
	return nil
}

func (t *rebroadcastTracker) should(txid string, now time.Time) bool {
	if t == nil || txid == "" {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.last == nil {
		t.last = map[string]time.Time{}
	}
	if prev, ok := t.last[txid]; ok && now.Sub(prev) < rebroadcastEvery {
		return false
	}
	t.last[txid] = now
	return true
}

func (e *Engine) maybeRebroadcast(ctx context.Context, ev arcadestatus.Event) {
	if e.watch == nil || ev.RawTx == "" {
		return
	}
	if e.retry == nil || !e.retry.should(ev.TxID, e.clock.Now()) {
		return
	}
	if err := e.watch.Rebroadcast(ctx, ev.RawTx); err != nil {
		e.log.Warn("rebroadcast parked tx", "txid", ev.TxID, "error", err.Error())
		return
	}
	e.log.Info("rebroadcast parked tx", "txid", ev.TxID, "status", ev.Status)
}
