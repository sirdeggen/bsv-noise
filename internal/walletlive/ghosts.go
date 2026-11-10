package walletlive

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/sirdeggen/bsv-noise/internal/arcadestatus"
)

// OnChainLookup reports whether a signed transaction actually exists on the
// network. Implementations should return (false, nil) for a definite miss
// (HTTP 404) and an error when the check itself failed.
type OnChainLookup func(ctx context.Context, txids []string) (map[string]bool, error)

// GhostRow is one user_utxos index entry whose parent action never became a
// spendable on-chain output (failed, aborted, unsigned, or unprocessed).
type GhostRow struct {
	OutputID uint64
	Satoshis uint64
	TxID     string
	Status   string
}

// ListFailedUTXOIndex returns change-index rows whose parent transaction is
// marked failed, aborted, unsigned, or unprocessed. These rows inflate
// CountUTXOs even when the outputs never existed on chain.
func ListFailedUTXOIndex(ctx context.Context, db *sql.DB) ([]GhostRow, error) {
	if db == nil {
		return nil, fmt.Errorf("wallet db is required")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT u.output_id, u.satoshis, IFNULL(t.tx_id, ''), t.status
		FROM bsv_user_utxos u
		JOIN bsv_outputs o ON o.id = u.output_id
		JOIN bsv_transactions t ON t.id = o.transaction_id
		WHERE t.status IN ('failed', 'aborted')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []GhostRow
	for rows.Next() {
		var r GhostRow
		if err := rows.Scan(&r.OutputID, &r.Satoshis, &r.TxID, &r.Status); err != nil {
			return nil, err
		}
		out = append(out, r)
	}
	return out, rows.Err()
}

// PruneResult is the outcome of a failed-UTXO index cleanup.
type PruneResult struct {
	Removed     int
	RemovedSats uint64
	Skipped     []string // txids kept because they appear on the network
	LookedUp    int
}

// PruneFailedUTXOIndex deletes user_utxos rows for failed, aborted, unsigned,
// or unprocessed parent transactions that are not on the network. It never
// deletes transaction or output history, and it refuses to drop a row when
// the lookup errors or reports the tx as present (including PENDING_RETRY /
// ACCEPTED). Funds that actually landed stay indexed so they can be spent.
func PruneFailedUTXOIndex(ctx context.Context, db *sql.DB, lookup OnChainLookup) (PruneResult, error) {
	var res PruneResult
	rows, err := ListFailedUTXOIndex(ctx, db)
	if err != nil {
		return res, err
	}
	if len(rows) == 0 {
		return res, nil
	}
	byTx := map[string][]GhostRow{}
	var untitled []GhostRow
	for _, r := range rows {
		if r.TxID == "" {
			untitled = append(untitled, r)
			continue
		}
		byTx[r.TxID] = append(byTx[r.TxID], r)
	}
	var txids []string
	for txid := range byTx {
		txids = append(txids, txid)
	}
	res.LookedUp = len(txids)
	present := map[string]bool{}
	if lookup != nil && len(txids) > 0 {
		present, err = lookup(ctx, txids)
		if err != nil {
			return res, fmt.Errorf("on-chain check failed; leaving failed UTXO index untouched: %w", err)
		}
	}
	var deleteIDs []uint64
	for txid, group := range byTx {
		if present[txid] {
			res.Skipped = append(res.Skipped, txid)
			continue
		}
		for _, r := range group {
			deleteIDs = append(deleteIDs, r.OutputID)
			res.RemovedSats += r.Satoshis
		}
	}
	// Failed rows with no txid were never broadcast; they cannot be on chain.
	for _, r := range untitled {
		deleteIDs = append(deleteIDs, r.OutputID)
		res.RemovedSats += r.Satoshis
	}
	if len(deleteIDs) == 0 {
		return res, nil
	}
	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return PruneResult{}, err
	}
	defer tx.Rollback()
	stmt, err := tx.PrepareContext(ctx, `DELETE FROM bsv_user_utxos WHERE output_id = ?`)
	if err != nil {
		return PruneResult{}, err
	}
	defer stmt.Close()
	for _, id := range deleteIDs {
		if _, err := stmt.ExecContext(ctx, id); err != nil {
			return PruneResult{}, err
		}
		res.Removed++
	}
	if err := tx.Commit(); err != nil {
		return PruneResult{}, err
	}
	return res, nil
}

// ArcadePresence treats any Arcade record other than a terminal rejection as
// "on chain or might still land." Only a complete miss (404 on every host)
// is safe to prune.
func ArcadePresence(client *arcadestatus.Client) OnChainLookup {
	return func(ctx context.Context, txids []string) (map[string]bool, error) {
		out := map[string]bool{}
		if client == nil {
			return out, fmt.Errorf("no arcade status client")
		}
		events, err := client.Query(ctx, txids)
		if err != nil {
			return nil, err
		}
		for _, ev := range events {
			if ev.TxID == "" {
				continue
			}
			if arcadestatus.IsRejected(ev.Status) {
				continue
			}
			out[ev.TxID] = true
		}
		return out, nil
	}
}

func logPrune(log *slog.Logger, res PruneResult) {
	if log == nil {
		log = slog.Default()
	}
	log.Info("pruned dead-tx UTXO index rows",
		"removed", res.Removed,
		"removed_sats", res.RemovedSats,
		"skipped_on_chain", len(res.Skipped),
		"looked_up", res.LookedUp,
	)
}

// EligibleCoin is a spendable default-basket output whose parent is already
// broadcast (unproven or completed) and has a real txid.
type EligibleCoin struct {
	TxID     string
	Vout     uint32
	Satoshis uint64
}

// ListEligibleSweepCoins returns unreserved, spendable coins that are safe to
// include in a dust sweep. Parents that are unsigned, unprocessed, sending,
// aborted, or failed are skipped — their outputs are not known to be on the
// network, and spending them produces CreateAction/BEEF failures.
func ListEligibleSweepCoins(ctx context.Context, db *sql.DB) ([]EligibleCoin, error) {
	if db == nil {
		return nil, fmt.Errorf("wallet db is required")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT t.tx_id, o.vout, u.satoshis
		FROM bsv_user_utxos u
		JOIN bsv_outputs o ON o.id = u.output_id
		JOIN bsv_transactions t ON t.id = o.transaction_id
		WHERE u.reserved_by_id IS NULL
		  AND o.spendable = 1
		  AND o.spent_by IS NULL
		  AND t.status IN ('completed', 'unproven')
		  AND t.tx_id IS NOT NULL AND TRIM(t.tx_id) != ''
		  AND u.satoshis > 0`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []EligibleCoin
	for rows.Next() {
		var c EligibleCoin
		if err := rows.Scan(&c.TxID, &c.Vout, &c.Satoshis); err != nil {
			return nil, err
		}
		out = append(out, c)
	}
	return out, rows.Err()
}

// StaleAbortAge is how long an unsigned/unprocessed sweep must sit with no
// txid before cleanup may abort it. Younger rows may still be mid-sign or
// mid-broadcast and must be left alone.
const StaleAbortAge = 15 * time.Minute

// StaleAction is a wallet transaction that never finished broadcasting.
type StaleAction struct {
	ID          uint64
	Status      string
	TxID        string
	Reference   string
	Description string
	CreatedAt   time.Time
}

// ListStaleActions returns unsigned, unprocessed, and aborted transactions.
func ListStaleActions(ctx context.Context, db *sql.DB) ([]StaleAction, error) {
	if db == nil {
		return nil, fmt.Errorf("wallet db is required")
	}
	rows, err := db.QueryContext(ctx, `
		SELECT id, status, IFNULL(tx_id, ''), IFNULL(reference, ''), IFNULL(description, ''), IFNULL(created_at, '')
		FROM bsv_transactions
		WHERE status IN ('unsigned', 'unprocessed', 'aborted')`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []StaleAction
	for rows.Next() {
		var a StaleAction
		var created string
		if err := rows.Scan(&a.ID, &a.Status, &a.TxID, &a.Reference, &a.Description, &created); err != nil {
			return nil, err
		}
		a.CreatedAt = parseSQLiteTime(created)
		out = append(out, a)
	}
	return out, rows.Err()
}

func parseSQLiteTime(s string) time.Time {
	s = strings.TrimSpace(s)
	if s == "" {
		return time.Time{}
	}
	// Toolbox stores RFC3339-ish timestamps, sometimes with a space.
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	if t, err := time.Parse(time.RFC3339, s); err == nil {
		return t
	}
	s = strings.Replace(s, " ", "T", 1)
	if t, err := time.Parse(time.RFC3339Nano, s); err == nil {
		return t
	}
	// Drop a trailing +00:00 / Z and parse as UTC local datetime.
	if i := strings.IndexAny(s, "Z+"); i > 0 {
		s = s[:i]
	}
	if t, err := time.Parse("2006-01-02T15:04:05.999999999", s); err == nil {
		return t.UTC()
	}
	return time.Time{}
}

func isPaymentAction(description string) bool {
	d := strings.ToLower(description)
	return strings.Contains(d, "load-test payment") || strings.Contains(d, "brc-29") && strings.Contains(d, "payment")
}

func isSweepAction(description string) bool {
	return strings.Contains(strings.ToLower(description), "dust sweep")
}

// StaleTxIDs is the set of non-empty txids on stale actions. Those must be
// checked on chain before aborting or pruning.
func StaleTxIDs(actions []StaleAction) []string {
	seen := map[string]struct{}{}
	var out []string
	for _, a := range actions {
		if a.TxID == "" {
			continue
		}
		if _, ok := seen[a.TxID]; ok {
			continue
		}
		seen[a.TxID] = struct{}{}
		out = append(out, a.TxID)
	}
	return out
}

// CanAbort reports whether AbortAction should be called.
// Payments are never aborted here — they may still be mid-broadcast.
// In-progress sweeps (a txid, or younger than StaleAbortAge) are left alone.
// Only a dust-sweep row with no txid, a reference, and a safe age may be aborted.
func CanAbort(a StaleAction, now time.Time, present map[string]bool, lookupOK bool) bool {
	if a.Status == "aborted" || a.Reference == "" {
		return false
	}
	if isPaymentAction(a.Description) {
		return false
	}
	if !isSweepAction(a.Description) {
		return false
	}
	if a.Status != "unsigned" && a.Status != "unprocessed" {
		return false
	}
	// A stored txid means ProcessAction ran; aborting can race a delayed
	// broadcast or hide real funds if the lookup is a stale 404.
	if a.TxID != "" {
		return false
	}
	if a.CreatedAt.IsZero() || now.IsZero() || now.Sub(a.CreatedAt) < StaleAbortAge {
		return false
	}
	return true
}
