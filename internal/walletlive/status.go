package walletlive

import (
	"context"

	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/monitor"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wdk"
	"github.com/sirdeggen/bsv-noise/internal/arcadestatus"
)

// eventTee forwards Arcade SSE frames to the loadgen after the monitor has
// been given the same event. The monitor remains the merkle-proof consumer.
type eventTee struct {
	inner monitor.BroadcastEventStreamer
	out   chan<- arcadestatus.Event
}

func (t *eventTee) BroadcastStatusEvents(ctx context.Context, lastEventID string, onEvent func(wdk.BroadcastStatusEvent) error) error {
	return t.inner.BroadcastStatusEvents(ctx, lastEventID, func(ev wdk.BroadcastStatusEvent) error {
		select {
		case t.out <- arcadestatus.Event{TxID: ev.TxID, Status: ev.Status}:
		default:
			// Drop when the engine is behind; GET /tx/:txid will catch up.
		}
		return onEvent(ev)
	})
}

// NetworkEvents is the teed Arcade SSE stream. It is never nil after Open.
func (w *Wallet) NetworkEvents() <-chan arcadestatus.Event {
	if w == nil {
		return nil
	}
	return w.netEvents
}

// QueryNetworkStatus is the GET /tx/:txid fallback for payments whose SSE
// frame has not arrived yet.
func (w *Wallet) QueryNetworkStatus(ctx context.Context, txids []string) ([]arcadestatus.Event, error) {
	if w == nil || w.statusClient == nil {
		return nil, nil
	}
	return w.statusClient.Query(ctx, txids)
}

// Rebroadcast posts a parked transaction (EF hex from GET /tx) to Arcade and
// any configured fallback hosts so a PENDING_RETRY row can reach the network.
func (w *Wallet) Rebroadcast(ctx context.Context, rawHex string) error {
	if w == nil || w.statusClient == nil {
		return nil
	}
	return w.statusClient.BroadcastHex(ctx, rawHex)
}
