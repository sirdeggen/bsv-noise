package store

import (
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

func openSim(t *testing.T) *Store {
	t.Helper()
	s, err := Open(filepath.Join(t.TempDir(), "loadgen.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = s.Close() })
	if err := s.EnsureGenesis("sim:genesis:0", 100_000); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestSpendConfirmRestartIdempotent(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loadgen.sqlite")
	s, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.EnsureGenesis("sim:genesis:0", 10_000); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 8, 12, 0, 0, 0, time.UTC)
	p, err := s.Begin(BeginRequest{
		ID: "p-1", Amount: 1000, Fee: 20, Recipient: "02aa", Reserve: 1000,
		DailySpendCap: 5000, DailyFeeCap: 500, MaxInputs: 4, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	if p.State != StateSending {
		t.Fatalf("state %s", p.State)
	}
	// Crash before finish. Reopen releases the reservation.
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	free, reserved, err := s.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if free != 10_000 || reserved != 0 {
		t.Fatalf("after abort free %d reserved %d", free, reserved)
	}
	got, ok, err := s.Get("p-1")
	if err != nil || !ok || got.State != StateAborted {
		t.Fatalf("restart state %+v ok %v err %v", got, ok, err)
	}

	p, err = s.Begin(BeginRequest{
		ID: "p-2", Amount: 1000, Fee: 20, Recipient: "02aa", Reserve: 1000,
		DailySpendCap: 5000, DailyFeeCap: 500, MaxInputs: 4, Now: now,
	})
	if err != nil {
		t.Fatal(err)
	}
	done, err := s.FinishAccepted("p-2", "sim:p-2", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if done.State != StateAccepted {
		t.Fatalf("accepted state %s", done.State)
	}
	again, err := s.FinishAccepted("p-2", "sim:p-2", 0, now)
	if err != nil {
		t.Fatal(err)
	}
	if again.TxID != done.TxID {
		t.Fatalf("second finish changed txid")
	}
	free, _, err = s.Balance()
	if err != nil {
		t.Fatal(err)
	}
	// 10000 - 1000 - 20 fee, change returned.
	if free != 8980 {
		t.Fatalf("balance %d", free)
	}
	if _, err := s.Begin(BeginRequest{
		ID: "p-2", Amount: 1000, Fee: 20, Recipient: "02aa", MaxInputs: 4, Now: now,
		DailySpendCap: 5000, DailyFeeCap: 500,
	}); !errors.Is(err, ErrConflict) {
		t.Fatalf("replay begin err %v", err)
	}
	n, err := s.ConfirmDue(now.Add(time.Second), 0)
	if err != nil || n != 1 {
		t.Fatalf("confirm %d %v", n, err)
	}
	c, err := s.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.Confirmed != 1 || c.Attempted < 1 {
		t.Fatalf("counts %+v", c)
	}
	// Accepted-but-not-confirmed was the state before ConfirmDue. A failed
	// broadcast must not have been counted as confirmed.
	if c.Failed != 0 {
		t.Fatalf("failed %+v", c)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	free2, _, err := s.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if free2 != free {
		t.Fatalf("restart balance %d want %d", free2, free)
	}
	got, ok, err = s.Get("p-2")
	if err != nil || !ok || got.State != StateConfirmed {
		t.Fatalf("persisted %+v", got)
	}
}

func TestCapsReserveAndFundingOnce(t *testing.T) {
	s := openSim(t)
	now := time.Date(2026, 11, 8, 0, 0, 0, 0, time.UTC)
	_, err := s.Begin(BeginRequest{
		ID: "too-big", Amount: 95_000, Fee: 10, Reserve: 10_000, MaxInputs: 4,
		DailySpendCap: 1_000_000, DailyFeeCap: 10_000, Now: now,
	})
	if !errors.Is(err, ErrInsufficientFunds) {
		t.Fatalf("reserve err %v", err)
	}
	if _, err := s.Begin(BeginRequest{
		ID: "cap", Amount: 100, Fee: 10, Reserve: 0, MaxInputs: 4,
		DailySpendCap: 50, DailyFeeCap: 10_000, Now: now,
	}); !errors.Is(err, ErrDailyCap) {
		t.Fatalf("cap err %v", err)
	}
	created, err := s.CreditFunding(Funding{MessageID: "m1", Sender: "02bb", TxID: "abc", Vout: 0, Satoshis: 500})
	if err != nil || !created {
		t.Fatalf("credit %v %v", created, err)
	}
	created, err = s.CreditFunding(Funding{MessageID: "m2", Sender: "02bb", TxID: "abc", Vout: 0, Satoshis: 500})
	if err != nil || created {
		t.Fatalf("second credit created=%v err=%v", created, err)
	}
	free, _, err := s.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if free != 100_500 {
		t.Fatalf("balance %d", free)
	}
}

func TestConcurrentReserveNoDoubleSpend(t *testing.T) {
	s := openSim(t)
	// Four coins. Each payment consumes one and leaves change too small to
	// fund another, so a double reservation would drop the success count
	// below four or the final balance would not match.
	if ok, err := s.Split(4, 20, "2026-11-08", 0); err != nil || !ok {
		t.Fatalf("split %v %v", ok, err)
	}
	now := time.Now().UTC()
	var wg sync.WaitGroup
	var mu sync.Mutex
	okN, badN := 0, 0
	for i := 0; i < 20; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			id, err := s.NextID()
			if err != nil {
				t.Error(err)
				return
			}
			_, err = s.Begin(BeginRequest{
				ID: id, Amount: 20_000, Fee: 10, Reserve: 0, MaxInputs: 1,
				DailySpendCap: 10_000_000, DailyFeeCap: 10_000_000, Now: now,
			})
			mu.Lock()
			defer mu.Unlock()
			if err == nil {
				if _, ferr := s.FinishAccepted(id, "sim:"+id, 0, now); ferr != nil {
					t.Error(ferr)
					return
				}
				okN++
				return
			}
			if errors.Is(err, ErrInsufficientFunds) {
				badN++
				return
			}
			t.Errorf("unexpected %v", err)
		}()
	}
	wg.Wait()
	if okN != 4 || badN != 16 {
		t.Fatalf("successes %d failures %d", okN, badN)
	}
	free, reserved, err := s.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if reserved != 0 {
		t.Fatalf("reserved %d", reserved)
	}
	// Genesis 100000, split fee 20, four payments of 20010.
	if free != 100_000-20-4*20_010 {
		t.Fatalf("free %d", free)
	}
}

func TestLiveInDoubtBlocksReplay(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "loadgen.sqlite")
	s, err := Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	if _, err := s.Begin(BeginRequest{
		ID: "live-1", Amount: 100, Fee: 5, Live: true, Available: 10_000,
		DailySpendCap: 10_000, DailyFeeCap: 1000, MaxInputs: 1, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if err := s.Close(); err != nil {
		t.Fatal(err)
	}
	s, err = Open(path, true)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	n, err := s.InDoubt()
	if err != nil || n != 1 {
		t.Fatalf("doubt %d %v", n, err)
	}
	_, err = s.Begin(BeginRequest{
		ID: "live-1", Amount: 100, Fee: 5, Live: true, Available: 10_000,
		DailySpendCap: 10_000, DailyFeeCap: 1000, Now: now,
	})
	if !errors.Is(err, ErrInDoubt) {
		t.Fatalf("replay %v", err)
	}
	_, err = s.Begin(BeginRequest{
		ID: "live-2", Amount: 100, Fee: 5, Live: true, Available: 10_000,
		DailySpendCap: 10_000, DailyFeeCap: 1000, Now: now,
	})
	if !errors.Is(err, ErrInDoubt) {
		t.Fatalf("new while doubt %v", err)
	}
}

func TestSplitAndConsolidate(t *testing.T) {
	s := openSim(t)
	ok, err := s.Split(4, 20, "2026-11-08", 10_000)
	if err != nil || !ok {
		t.Fatalf("split %v %v", ok, err)
	}
	free, total, err := s.UTXOCount()
	if err != nil {
		t.Fatal(err)
	}
	if free != 4 || total != 4 {
		t.Fatalf("count %d %d", free, total)
	}
	bal, _, err := s.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if bal != 100_000-20 {
		t.Fatalf("balance %d", bal)
	}
	ok, err = s.Consolidate(1, 10, "2026-11-08", 10_000)
	if err != nil || !ok {
		t.Fatalf("merge %v %v", ok, err)
	}
	free, _, err = s.UTXOCount()
	if err != nil || free != 1 {
		t.Fatalf("after merge %d %v", free, err)
	}
	bal, _, err = s.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if bal != 100_000-30 {
		t.Fatalf("balance after merge %d", bal)
	}
}

func TestSeenFreesInFlightAndKeepsConfirmedHonest(t *testing.T) {
	s := openSim(t)
	now := time.Date(2026, 11, 9, 21, 40, 0, 0, time.UTC)
	if _, err := s.Begin(BeginRequest{
		ID: "p-seen", Amount: 100, Fee: 5, Recipient: "02aa", Reserve: 0,
		DailySpendCap: 10_000, DailyFeeCap: 10_000, MaxInputs: 4, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	txid := "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	if _, err := s.FinishAccepted("p-seen", txid, 0, now); err != nil {
		t.Fatal(err)
	}
	c, err := s.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.InFlight != 1 || c.Seen != 0 || c.Confirmed != 0 || c.Accepted != 1 {
		t.Fatalf("after accept %+v", c)
	}
	changed, err := s.MarkSeen("p-seen", now.Add(time.Second))
	if err != nil || !changed {
		t.Fatalf("mark seen changed=%v err=%v", changed, err)
	}
	c, err = s.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.InFlight != 0 || c.Seen != 1 || c.Confirmed != 0 || c.Accepted != 1 {
		t.Fatalf("after seen %+v", c)
	}
	p, ok, err := s.LookupTxID(txid)
	if err != nil || !ok || p.State != StateSeen || p.Seen.IsZero() {
		t.Fatalf("lookup %+v ok=%v err=%v", p, ok, err)
	}
	if again, err := s.MarkSeen("p-seen", now.Add(2*time.Second)); err != nil || again {
		t.Fatalf("idempotent seen again=%v err=%v", again, err)
	}
	ids, err := s.AcceptedIDs(10)
	if err != nil || len(ids) != 1 || ids[0] != "p-seen" {
		t.Fatalf("accepted ids %v %v", ids, err)
	}
	unseen, err := s.UnseenBroadcast(10)
	if err != nil || len(unseen) != 0 {
		t.Fatalf("unseen after seen %v %v", unseen, err)
	}
	okConf, err := s.ConfirmID("p-seen", now.Add(time.Minute))
	if err != nil || !okConf {
		t.Fatalf("confirm %v %v", okConf, err)
	}
	c, err = s.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.InFlight != 0 || c.Seen != 1 || c.Confirmed != 1 || c.Accepted != 1 {
		t.Fatalf("after confirm %+v", c)
	}
}

func TestConfirmIDFromAcceptedSetsSeen(t *testing.T) {
	s := openSim(t)
	now := time.Date(2026, 11, 9, 21, 41, 0, 0, time.UTC)
	if _, err := s.Begin(BeginRequest{
		ID: "p-mined", Amount: 100, Fee: 5, Recipient: "02aa", Reserve: 0,
		DailySpendCap: 10_000, DailyFeeCap: 10_000, MaxInputs: 4, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishAccepted("p-mined", "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", 0, now); err != nil {
		t.Fatal(err)
	}
	ok, err := s.ConfirmID("p-mined", now.Add(time.Minute))
	if err != nil || !ok {
		t.Fatalf("confirm %v %v", ok, err)
	}
	p, found, err := s.Get("p-mined")
	if err != nil || !found || p.State != StateConfirmed || p.Seen.IsZero() {
		t.Fatalf("skipped-seen confirm %+v found=%v err=%v", p, found, err)
	}
	c, err := s.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.Seen != 1 || c.Confirmed != 1 || c.InFlight != 0 {
		t.Fatalf("counts %+v", c)
	}
}

func TestFailRejectedFreesSeenOrAccepted(t *testing.T) {
	s := openSim(t)
	now := time.Date(2026, 11, 9, 21, 42, 0, 0, time.UTC)
	for i, id := range []string{"p-acc", "p-seen"} {
		if _, err := s.Begin(BeginRequest{
			ID: id, Amount: 100, Fee: 5, Recipient: "02aa", Reserve: 0,
			DailySpendCap: 10_000, DailyFeeCap: 10_000, MaxInputs: 4, Now: now.Add(time.Duration(i) * time.Second),
		}); err != nil {
			t.Fatal(err)
		}
		if _, err := s.FinishAccepted(id, "cc"+id, 0, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := s.MarkSeen("p-seen", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail("p-acc", "REJECTED"); err != nil {
		t.Fatal(err)
	}
	if err := s.Fail("p-seen", "REJECTED"); err != nil {
		t.Fatal(err)
	}
	c, err := s.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.Failed != 2 || c.InFlight != 0 || c.Seen != 0 || c.Accepted != 0 {
		t.Fatalf("after reject %+v", c)
	}
	if err := s.Fail("p-acc", "REJECTED"); err != nil {
		t.Fatalf("idempotent fail: %v", err)
	}
}

func TestMigrateAddsSeenUnixOnExistingDB(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "legacy.sqlite")
	legacy, err := Open(path, false)
	if err != nil {
		t.Fatal(err)
	}
	// Simulate a pre-migration file by dropping the column... SQLite cannot
	// drop a column easily on all versions; instead insert a payment, close,
	// and reopen. ensureColumn must be idempotent on a current schema, and a
	// raw file created without the column must open.
	if err := legacy.Close(); err != nil {
		t.Fatal(err)
	}
	// Recreate a bare payment table without seen_unix.
	raw, err := Open(filepath.Join(dir, "bare.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	if err := raw.Close(); err != nil {
		t.Fatal(err)
	}
	// Build a truly old file: copy schema minus seen_unix via a second Open
	// after executing an old CREATE in a fresh path.
	oldPath := filepath.Join(dir, "old.sqlite")
	db, err := openLegacyPaymentDB(oldPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := db.Close(); err != nil {
		t.Fatal(err)
	}
	s, err := Open(oldPath, false)
	if err != nil {
		t.Fatal(err)
	}
	defer s.Close()
	now := time.Date(2026, 11, 9, 21, 43, 0, 0, time.UTC)
	if err := s.EnsureGenesis("sim:genesis:0", 10_000); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Begin(BeginRequest{
		ID: "p-legacy", Amount: 100, Fee: 5, Recipient: "02aa", Reserve: 0,
		DailySpendCap: 10_000, DailyFeeCap: 10_000, MaxInputs: 4, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := s.FinishAccepted("p-legacy", "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd", 0, now); err != nil {
		t.Fatal(err)
	}
	if _, err := s.MarkSeen("p-legacy", now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	got, ok, err := s.Get("p-legacy")
	if err != nil || !ok || got.State != StateSeen || got.Seen.IsZero() {
		t.Fatalf("migrated row %+v ok=%v err=%v", got, ok, err)
	}
}

func openLegacyPaymentDB(path string) (*Store, error) {
	// Create the file with the previous payment schema (no seen_unix), then
	// let Open/migrate add the column.
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path+"?_pragma=busy_timeout(5000)")
	if err != nil {
		return nil, err
	}
	_, err = db.Exec(`
CREATE TABLE payment (
  id TEXT PRIMARY KEY,
  created_unix INTEGER NOT NULL,
  accepted_unix INTEGER NOT NULL DEFAULT 0,
  confirmed_unix INTEGER NOT NULL DEFAULT 0,
  amount INTEGER NOT NULL,
  fee INTEGER NOT NULL,
  recipient TEXT NOT NULL,
  prefix TEXT NOT NULL,
  suffix TEXT NOT NULL,
  locking_script_hex TEXT NOT NULL,
  state TEXT NOT NULL,
  txid TEXT NOT NULL DEFAULT '',
  output_index INTEGER NOT NULL DEFAULT -1,
  error TEXT NOT NULL DEFAULT ''
);
CREATE TABLE meta (key TEXT PRIMARY KEY, value TEXT NOT NULL);
CREATE TABLE utxo (outpoint TEXT PRIMARY KEY, satoshis INTEGER NOT NULL, reserved_by TEXT NOT NULL DEFAULT '');
CREATE TABLE reservation (payment_id TEXT NOT NULL, outpoint TEXT NOT NULL, PRIMARY KEY (payment_id, outpoint));
CREATE TABLE funding (
  message_id TEXT PRIMARY KEY, sender TEXT NOT NULL, txid TEXT NOT NULL, vout INTEGER NOT NULL,
  satoshis INTEGER NOT NULL, state TEXT NOT NULL, error TEXT NOT NULL DEFAULT '', UNIQUE (txid, vout)
);
CREATE TABLE daily (day TEXT PRIMARY KEY, spend INTEGER NOT NULL DEFAULT 0, fees INTEGER NOT NULL DEFAULT 0, held_spend INTEGER NOT NULL DEFAULT 0, held_fees INTEGER NOT NULL DEFAULT 0);
`)
	_ = db.Close()
	if err != nil {
		return nil, err
	}
	return Open(path, false)
}
