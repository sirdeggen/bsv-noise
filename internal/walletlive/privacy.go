package walletlive

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	"github.com/bsv-blockchain/go-sdk/transaction"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wdk"
	"github.com/sirdeggen/bsv-noise/internal/dust"
)

const (
	p2pkhUnlockingScriptLength = 107
	// defaultDesiredUTXOs is a stable NumberOfDesiredUTXOs. Updating the
	// basket on every payment was a major source of SQLite write contention
	// with toolbox background broadcast. The funder emits up to
	// MaxChangeOutputsPerTx as long as existing coins are below this target.
	defaultDesiredUTXOs int64 = 128
)

// PrepareBasket sets the change basket once (or when the knobs change) so
// the next CreateAction is short of NumberOfDesiredUTXOs and the toolbox
// emits multiple change outputs. It no longer prunes the UTXO index on
// every payment — that happens at Open and before a scheduled sweep.
func (w *Wallet) PrepareBasket(ctx context.Context, targetChange int, minDesired uint64) error {
	if w == nil {
		return fmt.Errorf("wallet is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if targetChange < 1 {
		targetChange = 8
	}
	if uint64(targetChange) > 8 {
		targetChange = 8
	}
	if minDesired < 1 {
		minDesired = 1000
	}
	if w.basketDesired == defaultDesiredUTXOs && w.basketMin == minDesired && w.maxChangeSet == targetChange {
		return nil
	}
	if w.provider != nil {
		w.provider.SetMaxChangeOutputsPerTx(uint64(targetChange))
	}
	if w.provider == nil || w.identity == "" {
		return fmt.Errorf("wallet storage or identity is missing")
	}
	if err := w.provider.UpdateChangeBasket(ctx, w.identity, wdk.BasketConfiguration{
		Name:                    wdk.BasketNameForChange,
		NumberOfDesiredUTXOs:    defaultDesiredUTXOs,
		MinimumDesiredUTXOValue: minDesired,
	}); err != nil {
		return fmt.Errorf("update change basket: %w", err)
	}
	w.basketDesired = defaultDesiredUTXOs
	w.basketMin = minDesired
	w.maxChangeSet = targetChange
	return nil
}

func (w *Wallet) pruneFailedIndexLocked(ctx context.Context) error {
	sqlDB, err := w.openSQL()
	if err != nil || sqlDB == nil {
		return err
	}
	res, err := PruneFailedUTXOIndex(ctx, sqlDB, ArcadePresence(w.statusClient))
	if err != nil {
		return err
	}
	if res.Removed > 0 || len(res.Skipped) > 0 {
		logPrune(w.logger(), res)
	}
	return nil
}

func (w *Wallet) logger() *slog.Logger {
	if w != nil && w.log != nil {
		return w.log
	}
	return slog.Default()
}

// CleanupStaleActions aborts unsigned/unprocessed actions that never landed
// on the network and prunes their leftover user_utxos index rows. Actions
// whose txid is present on Arcade or WhatsOnChain are left alone — those
// may be real funds.
func (w *Wallet) CleanupStaleActions(ctx context.Context) error {
	if w == nil {
		return fmt.Errorf("wallet is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.cleanupStaleLocked(ctx)
}

func (w *Wallet) cleanupStaleLocked(ctx context.Context) error {
	sqlDB, err := w.openSQL()
	if err != nil || sqlDB == nil {
		return err
	}
	actions, err := ListStaleActions(ctx, sqlDB)
	if err != nil {
		return err
	}
	txids := StaleTxIDs(actions)
	lookupOK := true
	present := map[string]bool{}
	if len(txids) > 0 {
		present, err = ArcadePresence(w.statusClient)(ctx, txids)
		if err != nil {
			lookupOK = false
			w.logger().Warn("stale-action on-chain lookup failed; leaving txid rows untouched", "err", err, "txids", len(txids))
		}
	}
	var aborted, skipped, abortErrs int
	for _, a := range actions {
		if a.TxID != "" && lookupOK && present[a.TxID] {
			skipped++
			continue
		}
		if !CanAbort(a, time.Now(), present, lookupOK) {
			continue
		}
		if _, err := w.inner.AbortAction(ctx, sdk.AbortActionArgs{Reference: []byte(a.Reference)}, w.originator); err != nil {
			abortErrs++
			w.logger().Warn("abort stale action failed", "status", a.Status, "err", err)
			continue
		}
		aborted++
	}
	if aborted > 0 || skipped > 0 || abortErrs > 0 {
		w.logger().Info("cleaned stale wallet actions",
			"aborted", aborted,
			"skipped_on_chain", skipped,
			"abort_errors", abortErrs,
			"stale_rows", len(actions),
		)
	}
	if err := w.pruneFailedIndexLocked(ctx); err != nil {
		w.logger().Warn("UTXO index prune deferred", "err", err)
	}
	return nil
}

// SweepDust spends eligible dust as inputs to an OP_FALSE OP_RETURN output.
// It runs at most once per cooldown (or earlier if ForceCoins dust is
// waiting), batches up to DefaultMaxInputs in a single transaction, then
// stops. Coins whose parent is not yet unproven/completed are skipped.
// Leftover below the dust threshold is burned on the OP_RETURN so the
// toolbox cannot mint a new dust change output.
func (w *Wallet) SweepDust(ctx context.Context, threshold uint64, satPerKB int64) (int, error) {
	if w == nil {
		return 0, fmt.Errorf("wallet is required")
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	sqlDB, err := w.openSQL()
	if err != nil {
		return 0, err
	}
	if sqlDB == nil {
		return 0, nil
	}
	coins, err := listEligibleDustCoins(ctx, sqlDB)
	if err != nil {
		return 0, err
	}
	var eligibleDust int
	for _, c := range coins {
		if c.Satoshis < threshold {
			eligibleDust++
		}
	}
	if !dust.Ready(time.Now(), w.lastSweep, eligibleDust, dust.DefaultCooldown, dust.ForceCoins) {
		return 0, nil
	}
	// Release reservations from leftover unsigned sweeps before creating another.
	if err := w.cleanupStaleLocked(ctx); err != nil {
		w.logger().Warn("pre-sweep stale cleanup failed", "err", err)
	}
	coins, err = listEligibleDustCoins(ctx, sqlDB)
	if err != nil {
		return 0, err
	}
	plan, err := dust.Select(coins, threshold, satPerKB)
	if err != nil {
		// Do not retry every payment when dust cannot cover the fee.
		w.lastSweep = time.Now()
		return 0, err
	}
	if len(plan.Inputs) == 0 {
		w.lastSweep = time.Now()
		return 0, nil
	}
	scriptBytes, err := dust.FalseReturnScript()
	if err != nil {
		return 0, err
	}
	// Do not pass Inputs: a caller-provided input is assembled as "from args"
	// and gets no BRC-29 unlocking template, so SignAction cannot sign it.
	// The privacy funder selects smallest-first, which is the same dust
	// batch Select already computed; OpReturnSats is sized so leftover
	// stays under the toolbox dustFloor.
	falseVal := false
	trueVal := true
	trust := sdk.TrustSelfKnown
	created, err := w.inner.CreateAction(ctx, sdk.CreateActionArgs{
		Description: "synthetic dust sweep",
		Outputs: []sdk.CreateActionOutput{{
			LockingScript:     scriptBytes,
			Satoshis:          plan.OpReturnSats,
			OutputDescription: "dust sweep OP_RETURN",
			Tags:              []string{"dust-sweep"},
		}},
		Labels: []string{"dust-sweep"},
		Options: &sdk.CreateActionOptions{
			SignAndProcess:         &trueVal,
			RandomizeOutputs:       &trueVal,
			AcceptDelayedBroadcast: &falseVal,
			TrustSelf:              trust,
		},
	}, w.originator)
	if err != nil {
		w.lastSweep = time.Now()
		return 0, fmt.Errorf("dust sweep: %w", err)
	}
	txid, send, err := finishSweepAction(ctx, w.inner, created, w.originator)
	if err != nil {
		w.lastSweep = time.Now()
		return 0, fmt.Errorf("dust sweep: %w", err)
	}
	w.lastSweep = time.Now()
	if !SweepBroadcasted(txid, send) {
		w.logger().Warn("dust sweep created but not broadcast",
			"inputs", len(plan.Inputs),
			"txid", txid.String(),
		)
		return 0, nil
	}
	w.logger().Info("dust sweep broadcast",
		"inputs", len(plan.Inputs),
		"txid", txid.String(),
		"funding_sats", plan.FundingSats,
		"op_return_sats", plan.OpReturnSats,
		"fee_est", plan.Fee,
		"extra_input", plan.ExtraInput,
	)
	return len(plan.Inputs), nil
}

// finishSweepAction completes a CreateAction that returned a signable
// transaction (wallet inputs without unlocking scripts are always a
// SignAction). Payments that do not pass Inputs are processed inside
// CreateAction and will already have a txid here.
func finishSweepAction(ctx context.Context, inner sweepSigner, created *sdk.CreateActionResult, originator string) (chainhash.Hash, []sdk.SendWithResult, error) {
	var zero chainhash.Hash
	if created == nil {
		return zero, nil, fmt.Errorf("wallet returned no create result")
	}
	if created.SignableTransaction != nil && len(created.SignableTransaction.Reference) > 0 {
		falseVal := false
		signed, err := inner.SignAction(ctx, sdk.SignActionArgs{
			Reference: created.SignableTransaction.Reference,
			Options:   &sdk.SignActionOptions{AcceptDelayedBroadcast: &falseVal},
		}, originator)
		if err != nil {
			return zero, nil, err
		}
		if signed == nil {
			return zero, nil, fmt.Errorf("sign action returned no result")
		}
		return signed.Txid, signed.SendWithResults, nil
	}
	return created.Txid, created.SendWithResults, nil
}

// SweepBroadcasted is true when the wallet handed back a real txid and no
// send result reported a failure. Delayed/sending counts as broadcast for
// the dust_swept counter; a zero txid or a failed status does not.
func SweepBroadcasted(txid chainhash.Hash, results []sdk.SendWithResult) bool {
	var zero chainhash.Hash
	if txid.IsEqual(&zero) {
		return false
	}
	for _, r := range results {
		if r.Status == sdk.ActionResultStatusFailed {
			return false
		}
	}
	return true
}

type sweepSigner interface {
	SignAction(ctx context.Context, args sdk.SignActionArgs, originator string) (*sdk.SignActionResult, error)
}

func (w *Wallet) openSQL() (*sql.DB, error) {
	if w.provider == nil || w.provider.Database == nil || w.provider.Database.DB == nil {
		return nil, nil
	}
	return w.provider.Database.DB.DB()
}

// CompletedTxIDs reports which of the given txids have a wallet action
// status of completed (mined with a merkle path). Used by the confirm
// poll so it can mark loadgen payments without getting stuck on the
// oldest still-unproven rows.
func (w *Wallet) CompletedTxIDs(ctx context.Context, txids []string) (map[string]bool, error) {
	out := map[string]bool{}
	if w == nil || len(txids) == 0 {
		return out, nil
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	sqlDB, err := w.openSQL()
	if err != nil {
		return nil, err
	}
	if sqlDB == nil {
		return out, nil
	}
	const chunk = 400
	for i := 0; i < len(txids); i += chunk {
		end := i + chunk
		if end > len(txids) {
			end = len(txids)
		}
		part := txids[i:end]
		placeholders := make([]string, len(part))
		args := make([]any, 0, len(part)+1)
		args = append(args, "completed")
		for j, id := range part {
			placeholders[j] = "?"
			args = append(args, id)
		}
		q := `SELECT tx_id FROM bsv_transactions WHERE status = ? AND tx_id IN (` + strings.Join(placeholders, ",") + `)`
		rows, err := sqlDB.QueryContext(ctx, q, args...)
		if err != nil {
			return nil, err
		}
		for rows.Next() {
			var id string
			if err := rows.Scan(&id); err != nil {
				rows.Close()
				return nil, err
			}
			if id != "" {
				out[id] = true
			}
		}
		err = rows.Err()
		rows.Close()
		if err != nil {
			return nil, err
		}
	}
	return out, nil
}

func listEligibleDustCoins(ctx context.Context, sqlDB *sql.DB) ([]dust.Coin, error) {
	rows, err := ListEligibleSweepCoins(ctx, sqlDB)
	if err != nil {
		return nil, err
	}
	var coins []dust.Coin
	for _, r := range rows {
		txid, err := chainhash.NewHashFromHex(r.TxID)
		if err != nil {
			continue
		}
		coins = append(coins, dust.Coin{
			Satoshis: r.Satoshis,
			Input: sdk.CreateActionInput{
				Outpoint: transaction.Outpoint{
					Txid:  *txid,
					Index: r.Vout,
				},
				InputDescription:      "dust sweep input",
				UnlockingScriptLength: p2pkhUnlockingScriptLength,
			},
		})
	}
	return coins, nil
}
