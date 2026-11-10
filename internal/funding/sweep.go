// Package funding imports Message Box payments into the spending wallet.
//
// Acknowledge runs only after the wallet accepts the output, or after the
// dry-run ledger records it. A failed import leaves the message on the server
// so a restart can retry. An outpoint that was already imported is not
// credited twice; the duplicate message is acknowledged so the box can drop it.
package funding

import (
	"context"
	"errors"

	"github.com/sirdeggen/bsv-noise/internal/messagebox"
	"github.com/sirdeggen/bsv-noise/internal/peerpay"
	"github.com/sirdeggen/bsv-noise/internal/store"
)

// Importer accepts one parsed payment. Live mode calls wallet.InternalizeAction.
type Importer interface {
	Internalize(ctx context.Context, in peerpay.Incoming) (accepted bool, err error)
}

// Result is one sweep pass.
type Result struct {
	Listed    int
	Imported  int
	Acked     int
	Failed    int
	Malformed int
	Pending   int
}

// SweepOnce lists the box, imports new payments, and acknowledges the ones
// that were accepted. creditLedger adds a dry-run coin. Live mode passes
// false because the wallet balance is the source of truth.
func SweepOnce(ctx context.Context, box *messagebox.Client, st *store.Store, imp Importer, creditLedger bool) (Result, error) {
	var result Result
	if box == nil || imp == nil || st == nil {
		return result, errors.New("sweep is missing message box, store, or importer")
	}
	incoming, failed, err := box.ListPayments(ctx)
	if err != nil {
		return result, err
	}
	result.Listed = len(incoming) + len(failed)
	result.Malformed = len(failed)
	for _, f := range failed {
		reason := "unreadable payment"
		if f.Err != nil {
			reason = truncate(f.Err.Error(), 200)
		}
		_ = st.MarkFundingError(f.MessageID, f.Sender, "", 0, 0, reason)
	}
	for _, in := range incoming {
		if err := handle(ctx, box, st, imp, creditLedger, in, &result); err != nil {
			return result, err
		}
	}
	return result, nil
}

func handle(ctx context.Context, box *messagebox.Client, st *store.Store, imp Importer, credit bool, in peerpay.Incoming, result *Result) error {
	existing, ok, err := st.FundingState(in.MessageID)
	if err != nil {
		return err
	}
	if ok && existing.State == store.FundAcknowledged {
		return nil
	}
	if ok && (existing.State == store.FundInternalized || existing.State == store.FundAckPending) {
		if err := ack(ctx, box, st, in.MessageID); err != nil {
			result.Pending++
			return nil
		}
		result.Acked++
		return nil
	}
	dup, err := st.OutpointFunded(in.TxID, int(in.OutputIndex))
	if err != nil {
		return err
	}
	if dup {
		if err := ack(ctx, box, st, in.MessageID); err != nil {
			result.Pending++
			return nil
		}
		result.Acked++
		return nil
	}
	accepted, err := imp.Internalize(ctx, in)
	if err != nil || !accepted {
		reason := "wallet did not accept the payment"
		if err != nil {
			reason = truncate(err.Error(), 200)
		}
		_ = st.MarkFundingError(in.MessageID, in.Sender, in.TxID, int(in.OutputIndex), int64(in.Satoshis), reason)
		result.Failed++
		return nil
	}
	if _, err := st.SaveFunding(toFunding(in), credit); err != nil {
		return err
	}
	result.Imported++
	if err := ack(ctx, box, st, in.MessageID); err != nil {
		result.Pending++
		return nil
	}
	result.Acked++
	return nil
}

func ack(ctx context.Context, box *messagebox.Client, st *store.Store, id string) error {
	if err := box.Acknowledge(ctx, []string{id}); err != nil {
		_ = st.NoteFundingAckPending(id)
		return err
	}
	return st.MarkFundingAck(id)
}

func toFunding(in peerpay.Incoming) store.Funding {
	return store.Funding{
		MessageID: in.MessageID,
		Sender:    in.Sender,
		TxID:      in.TxID,
		Vout:      int(in.OutputIndex),
		Satoshis:  int64(in.Satoshis),
		State:     store.FundInternalized,
	}
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
