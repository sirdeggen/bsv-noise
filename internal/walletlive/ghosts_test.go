package walletlive

import (
	"context"
	"database/sql"
	"testing"
	"time"

	_ "modernc.org/sqlite"
)

func setupGhostDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", "file:ghosts?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE bsv_transactions (id INTEGER PRIMARY KEY, tx_id TEXT, status TEXT)`,
		`CREATE TABLE bsv_outputs (id INTEGER PRIMARY KEY, transaction_id INTEGER, satoshis INTEGER)`,
		`CREATE TABLE bsv_user_utxos (output_id INTEGER PRIMARY KEY, satoshis INTEGER)`,
		`INSERT INTO bsv_transactions (id, tx_id, status) VALUES
			(1, 'aa', 'failed'),
			(2, 'bb', 'failed'),
			(3, 'cc', 'unproven')`,
		`INSERT INTO bsv_outputs (id, transaction_id, satoshis) VALUES
			(10, 1, 100),
			(11, 1, 200),
			(20, 2, 999),
			(30, 3, 50)`,
		`INSERT INTO bsv_user_utxos (output_id, satoshis) VALUES
			(10, 100), (11, 200), (20, 999), (30, 50)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	return db
}

func TestPruneRemovesOnlyOffChainFailed(t *testing.T) {
	db := setupGhostDB(t)
	lookup := func(_ context.Context, txids []string) (map[string]bool, error) {
		m := map[string]bool{}
		for _, id := range txids {
			if id == "bb" {
				m[id] = true // this failed tx actually landed
			}
		}
		return m, nil
	}
	res, err := PruneFailedUTXOIndex(context.Background(), db, lookup)
	if err != nil {
		t.Fatal(err)
	}
	if res.Removed != 2 || res.RemovedSats != 300 {
		t.Fatalf("removed %+v", res)
	}
	if len(res.Skipped) != 1 || res.Skipped[0] != "bb" {
		t.Fatalf("skipped %v", res.Skipped)
	}
	var n, leftover int
	_ = db.QueryRow(`SELECT COUNT(*), SUM(satoshis) FROM bsv_user_utxos`).Scan(&n, &leftover)
	if n != 2 || leftover != 999+50 {
		t.Fatalf("remaining n=%d sats=%d", n, leftover)
	}
	// History stays.
	var txs int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bsv_transactions`).Scan(&txs)
	if txs != 3 {
		t.Fatalf("transactions deleted: %d", txs)
	}
}

func TestPruneStopsWhenLookupFails(t *testing.T) {
	db := setupGhostDB(t)
	_, err := PruneFailedUTXOIndex(context.Background(), db, func(context.Context, []string) (map[string]bool, error) {
		return nil, context.DeadlineExceeded
	})
	if err == nil {
		t.Fatal("expected error")
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bsv_user_utxos`).Scan(&n)
	if n != 4 {
		t.Fatalf("index was mutated after a failed lookup: %d", n)
	}
}

func TestPruneAbortedAndUnsignedWithoutTxid(t *testing.T) {
	db := setupGhostDB(t)
	for _, q := range []string{
		`INSERT INTO bsv_transactions (id, tx_id, status) VALUES (4, '', 'aborted'), (5, '', 'unsigned')`,
		`INSERT INTO bsv_outputs (id, transaction_id, satoshis) VALUES (40, 4, 10), (50, 5, 20)`,
		`INSERT INTO bsv_user_utxos (output_id, satoshis) VALUES (40, 10), (50, 20)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	res, err := PruneFailedUTXOIndex(context.Background(), db, func(context.Context, []string) (map[string]bool, error) {
		return map[string]bool{"bb": true}, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	// failed aa (2 rows, 300) + aborted 10 = 3 rows, 310 sats. unsigned is left
	// until it is actually aborted — it may still be mid-sign/broadcast.
	if res.Removed != 3 || res.RemovedSats != 310 {
		t.Fatalf("removed %+v", res)
	}
	var n, unsigned int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bsv_user_utxos`).Scan(&n)
	_ = db.QueryRow(`SELECT COUNT(*) FROM bsv_user_utxos WHERE output_id = 50`).Scan(&unsigned)
	if n != 3 || unsigned != 1 { // bb + unproven + unsigned
		t.Fatalf("remaining %d unsigned_index=%d", n, unsigned)
	}
}

func TestPruneKeepsAbortedPaymentOnChain(t *testing.T) {
	db := setupGhostDB(t)
	for _, q := range []string{
		`INSERT INTO bsv_transactions (id, tx_id, status) VALUES (6, 'paytxid', 'aborted')`,
		`INSERT INTO bsv_outputs (id, transaction_id, satoshis) VALUES (60, 6, 500)`,
		`INSERT INTO bsv_user_utxos (output_id, satoshis) VALUES (60, 500)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	res, err := PruneFailedUTXOIndex(context.Background(), db, func(_ context.Context, txids []string) (map[string]bool, error) {
		m := map[string]bool{"bb": true}
		for _, id := range txids {
			if id == "paytxid" {
				m[id] = true
			}
		}
		return m, nil
	})
	if err != nil {
		t.Fatal(err)
	}
	kept := false
	for _, id := range res.Skipped {
		if id == "paytxid" {
			kept = true
		}
	}
	if !kept {
		t.Fatalf("on-chain aborted payment was not skipped: %+v", res)
	}
	var n int
	_ = db.QueryRow(`SELECT COUNT(*) FROM bsv_user_utxos WHERE output_id = 60`).Scan(&n)
	if n != 1 {
		t.Fatal("on-chain payment UTXO was deleted")
	}
}

func TestCanAbortPolicy(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 30, 0, 0, time.UTC)
	oldSweep := StaleAction{
		Status:      "unsigned",
		Reference:   "ref",
		Description: "synthetic dust sweep",
		CreatedAt:   now.Add(-20 * time.Minute),
	}
	if !CanAbort(oldSweep, now, nil, false) {
		t.Fatal("old unsigned sweep with no txid should abort")
	}
	young := oldSweep
	young.CreatedAt = now.Add(-time.Minute)
	if CanAbort(young, now, nil, false) {
		t.Fatal("in-progress sweep must not be aborted")
	}
	pay := StaleAction{
		Status:      "unprocessed",
		Reference:   "ref",
		Description: "synthetic BRC-29 load-test payment",
		CreatedAt:   now.Add(-time.Hour),
		TxID:        "missing",
	}
	if CanAbort(pay, now, nil, true) {
		t.Fatal("payments must never be aborted by this cleanup")
	}
	withTx := oldSweep
	withTx.TxID = "abc"
	if CanAbort(withTx, now, nil, true) {
		t.Fatal("sweep with a txid must not be aborted")
	}
	if CanAbort(StaleAction{Status: "aborted", Reference: "ref", Description: "synthetic dust sweep", CreatedAt: now.Add(-time.Hour)}, now, nil, true) {
		t.Fatal("already aborted should not be aborted again")
	}
	if CanAbort(StaleAction{Status: "unsigned", Description: "synthetic dust sweep", CreatedAt: now.Add(-time.Hour)}, now, nil, true) {
		t.Fatal("no reference cannot abort")
	}
	zeroTime := oldSweep
	zeroTime.CreatedAt = time.Time{}
	if CanAbort(zeroTime, now, nil, false) {
		t.Fatal("missing created_at must not abort")
	}
}

func TestListEligibleSkipsUnbroadcastParents(t *testing.T) {
	db, err := sql.Open("sqlite", "file:eligible?mode=memory&cache=shared")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	for _, q := range []string{
		`CREATE TABLE bsv_transactions (id INTEGER PRIMARY KEY, tx_id TEXT, status TEXT, reference TEXT, description TEXT)`,
		`CREATE TABLE bsv_outputs (id INTEGER PRIMARY KEY, transaction_id INTEGER, vout INTEGER, satoshis INTEGER, spendable INTEGER, spent_by INTEGER)`,
		`CREATE TABLE bsv_user_utxos (output_id INTEGER PRIMARY KEY, satoshis INTEGER, reserved_by_id INTEGER)`,
		`INSERT INTO bsv_transactions (id, tx_id, status) VALUES
			(1, 'aa', 'unproven'),
			(2, 'bb', 'completed'),
			(3, 'cc', 'aborted'),
			(4, '', 'unsigned'),
			(5, 'ee', 'sending')`,
		`INSERT INTO bsv_outputs (id, transaction_id, vout, satoshis, spendable, spent_by) VALUES
			(10, 1, 0, 80, 1, NULL),
			(20, 2, 1, 5000, 1, NULL),
			(30, 3, 0, 80, 1, NULL),
			(40, 4, 0, 80, 1, NULL),
			(50, 5, 0, 80, 1, NULL)`,
		`INSERT INTO bsv_user_utxos (output_id, satoshis, reserved_by_id) VALUES
			(10, 80, NULL), (20, 5000, NULL), (30, 80, NULL), (40, 80, NULL), (50, 80, NULL)`,
	} {
		if _, err := db.Exec(q); err != nil {
			t.Fatal(err)
		}
	}
	coins, err := ListEligibleSweepCoins(context.Background(), db)
	if err != nil {
		t.Fatal(err)
	}
	if len(coins) != 2 {
		t.Fatalf("got %+v", coins)
	}
	seen := map[string]bool{}
	for _, c := range coins {
		seen[c.TxID] = true
	}
	if !seen["aa"] || !seen["bb"] {
		t.Fatalf("eligible %+v", coins)
	}
}
