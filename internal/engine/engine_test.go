package engine

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sirdeggen/bsv-noise/internal/arcadestatus"
	"github.com/sirdeggen/bsv-noise/internal/config"
	"github.com/sirdeggen/bsv-noise/internal/schedule"
	"github.com/sirdeggen/bsv-noise/internal/store"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
)

func TestDryRunPaysFromFundingAndDoesNotBurst(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	dir := t.TempDir()
	st, err := store.Open(filepath.Join(dir, "loadgen.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.EnsureGenesis("sim:genesis:0", 0); err != nil {
		t.Fatal(err)
	}
	created, err := st.CreditFunding(store.Funding{
		MessageID: "fund-1", Sender: "02aa", TxID: "fundtx", Vout: 0, Satoshis: 20_000,
	})
	if err != nil || !created {
		t.Fatalf("credit %v %v", created, err)
	}
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	recip, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	rt := config.Defaults()
	rt.TargetTPS = 50
	rt.MinAmount = 100
	rt.MaxAmount = 100
	rt.Seed = 11
	rt.ReserveSat = 1000
	rt.ConfirmAfter = "1h"
	rt.MaxInFlight = 1
	rt.MaxPayments = 3
	rt.MinUTXOs = 1
	rt.MaxUTXOs = 8
	rt.SimBalance = 20_000
	rt.DailySpendCap = 100_000
	rt.DailyFeeCap = 100_000
	cfg, err := rt.Validate()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Private = key
	cfg.Identity = key.PubKey().ToDERHex()
	clock := schedule.NewFakeClock(time.Unix(1_700_000_000, 0))
	eng, err := New(Options{
		Runtime: cfg, Store: st, Recipients: []*ec.PublicKey{recip.PubKey()},
		Clock: clock, Poll: 100 * time.Millisecond,
	})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- eng.Run(ctx) }()

	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		c, err := st.Counts()
		if err != nil {
			t.Fatal(err)
		}
		if c.Accepted >= 1 {
			break
		}
		clock.Advance(50 * time.Millisecond)
		time.Sleep(2 * time.Millisecond)
	}
	c, err := st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.Accepted != 1 || c.Confirmed != 0 {
		t.Fatalf("after first slot %+v", c)
	}
	// Several mean intervals pass while the single in-flight payment is
	// unconfirmed. That must not emit a catch-up burst.
	for i := 0; i < 30; i++ {
		clock.Advance(100 * time.Millisecond)
		time.Sleep(time.Millisecond)
	}
	c, err = st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.Accepted != 1 {
		t.Fatalf("burst accepted %d", c.Accepted)
	}
	// Confirmation delay is one hour. Advance past it and allow the pacer to
	// place the remaining payments one at a time.
	for i := 0; i < 40 && c.Accepted < 3; i++ {
		clock.Advance(time.Hour)
		time.Sleep(5 * time.Millisecond)
		c, err = st.Counts()
		if err != nil {
			t.Fatal(err)
		}
	}
	if c.Accepted != 3 {
		t.Fatalf("accepted %d inflight %d confirmed %d", c.Accepted, c.InFlight, c.Confirmed)
	}
	cancel()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("engine did not stop after cancel")
	}
	c, err = st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.Accepted != 3 || c.Confirmed < 1 {
		t.Fatalf("final %+v", c)
	}
	free, _, err := st.Balance()
	if err != nil {
		t.Fatal(err)
	}
	// 20000 funded, 3 payments of 100 plus the standard fee each.
	want := int64(20_000 - 3*(100+cfg.StandardFee))
	if free != want {
		t.Fatalf("balance %d want %d", free, want)
	}
	// A second credit of the same funding outpoint must not inflate the balance.
	if created, err := st.CreditFunding(store.Funding{MessageID: "fund-1b", Sender: "02aa", TxID: "fundtx", Vout: 0, Satoshis: 20_000}); err != nil || created {
		t.Fatalf("double credit created=%v err=%v", created, err)
	}
	free2, _, err := st.Balance()
	if err != nil {
		t.Fatal(err)
	}
	if free2 != free {
		t.Fatalf("double credit changed balance %d -> %d", free, free2)
	}
}

func TestStopsWhenFundsCannotCoverReserve(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	st, err := store.Open(filepath.Join(t.TempDir(), "loadgen.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.EnsureGenesis("sim:genesis:0", 500); err != nil {
		t.Fatal(err)
	}
	key, _ := ec.NewPrivateKey()
	recip, _ := ec.NewPrivateKey()
	rt := config.Defaults()
	rt.TargetTPS = 20
	rt.MinAmount = 100
	rt.MaxAmount = 100
	rt.Seed = 3
	rt.ReserveSat = 450
	rt.ConfirmAfter = "1ms"
	rt.MaxInFlight = 2
	rt.MaxPayments = 5
	rt.SimBalance = 100_000
	cfg, err := rt.Validate()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Private = key
	cfg.Identity = key.PubKey().ToDERHex()
	clock := schedule.NewFakeClock(time.Unix(0, 0))
	eng, err := New(Options{Runtime: cfg, Store: st, Recipients: []*ec.PublicKey{recip.PubKey()}, Clock: clock, Poll: 20 * time.Millisecond})
	if err != nil {
		t.Fatal(err)
	}
	go func() { _ = eng.Run(ctx) }()
	for i := 0; i < 50; i++ {
		clock.Advance(time.Second)
		time.Sleep(2 * time.Millisecond)
	}
	cancel()
	c, err := st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.Accepted != 0 || c.Confirmed != 0 {
		t.Fatalf("spent despite reserve %+v", c)
	}
	if eng.Status().Paused != "insufficient_funds" {
		t.Fatalf("paused %q", eng.Status().Paused)
	}
}

func TestStatsReportsDryRunCountWithoutSecrets(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "loadgen.sqlite"), false)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	if err := st.EnsureGenesis("sim:genesis:0", 50_000); err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 11, 9, 4, 0, 0, 0, time.UTC)
	if _, err := st.Begin(store.BeginRequest{
		ID: "p-1", Amount: 100, Fee: 23, Reserve: 0, MaxInputs: 4,
		DailySpendCap: 1_000_000, DailyFeeCap: 10_000, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishAccepted("p-1", "sim:p-1", 0, now); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishAccepted("p-live", "aabbccddeeff00112233445566778899aabbccddeeff00112233445566778899", 0, now); err == nil {
		t.Fatal("finished a payment that was never begun")
	}
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	recip, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	rt := config.Defaults()
	rt.SimBalance = 50_000
	rt.ReserveSat = 0
	cfg, err := rt.Validate()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Private = key
	cfg.Identity = key.PubKey().ToDERHex()
	eng, err := New(Options{Runtime: cfg, Store: st, Recipients: []*ec.PublicKey{recip.PubKey()}})
	if err != nil {
		t.Fatal(err)
	}
	doc, err := eng.Stats()
	if err != nil {
		t.Fatal(err)
	}
	if doc.Transactions.DryRun != 1 || doc.Transactions.Broadcast != 0 || doc.Transactions.Total != 1 {
		t.Fatalf("counts %+v", doc.Transactions)
	}
	if doc.BalanceBSV == "" || doc.Recent[0].TxID != "sim:p-1" || doc.Recent[0].ExplorerURL != "" {
		t.Fatalf("doc balance %s recent %+v", doc.BalanceBSV, doc.Recent)
	}
	raw, _ := json.Marshal(doc)
	if strings.Contains(string(raw), key.Hex()) {
		t.Fatal("stats contained the private key")
	}
	if doc.TPS.ExponentialVarianceSeconds2 <= 0 || doc.TPS.State == "" {
		t.Fatalf("tps %+v", doc.TPS)
	}
}

type fakeWatcher struct {
	ch    chan arcadestatus.Event
	query []arcadestatus.Event
	err   error
}

func (f *fakeWatcher) NetworkEvents() <-chan arcadestatus.Event { return f.ch }
func (f *fakeWatcher) QueryNetworkStatus(context.Context, []string) ([]arcadestatus.Event, error) {
	return f.query, f.err
}
func (f *fakeWatcher) Rebroadcast(context.Context, string) error { return nil }

func liveAccepted(t *testing.T, st *store.Store, id, txid string) {
	t.Helper()
	now := time.Unix(1_700_000_000, 0)
	if _, err := st.Begin(store.BeginRequest{
		ID: id, Amount: 100, Fee: 5, Recipient: "02aa", Live: true, Available: 1_000_000,
		DailySpendCap: 10_000_000, DailyFeeCap: 10_000, Now: now,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := st.FinishAccepted(id, txid, 0, now); err != nil {
		t.Fatal(err)
	}
}

func testEngineWithStore(t *testing.T, st *store.Store, watch NetworkWatcher) *Engine {
	t.Helper()
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	recip, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	rt := config.Defaults()
	rt.Broadcast = true
	rt.TargetTPS = 0.5
	rt.MaxInFlight = 4
	rt.MinAmount = 100
	rt.MaxAmount = 100
	cfg, err := rt.Validate()
	if err != nil {
		t.Fatal(err)
	}
	cfg.Private = key
	cfg.Identity = key.PubKey().ToDERHex()
	eng, err := New(Options{
		Runtime: cfg, Store: st, Recipients: []*ec.PublicKey{recip.PubKey()},
		Watcher: watch, Clock: schedule.NewFakeClock(time.Unix(1_700_000_100, 0)),
	})
	if err != nil {
		t.Fatal(err)
	}
	return eng
}

func TestApplyNetworkEventFreesInFlight(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "loadgen.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const seenTx = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	const earlyTx = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	const rejTx = "cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	liveAccepted(t, st, "p-seen", seenTx)
	liveAccepted(t, st, "p-early", earlyTx)
	liveAccepted(t, st, "p-rej", rejTx)
	eng := testEngineWithStore(t, st, &fakeWatcher{})
	eng.applyNetworkEvent(context.Background(), arcadestatus.Event{TxID: earlyTx, Status: "ACCEPTED_BY_NETWORK"})
	eng.applyNetworkEvent(context.Background(), arcadestatus.Event{TxID: seenTx, Status: "SEEN_ON_NETWORK"})
	eng.applyNetworkEvent(context.Background(), arcadestatus.Event{TxID: rejTx, Status: "REJECTED"})
	c, err := st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.InFlight != 1 || c.Seen != 1 || c.Failed != 1 || c.Confirmed != 0 || c.Accepted != 2 {
		t.Fatalf("counts after events %+v", c)
	}
	// Only accepted-but-not-seen occupies MaxInFlight.
	if int(c.InFlight) >= eng.rt.MaxInFlight {
		t.Fatalf("slot should be free: inflight %d max %d", c.InFlight, eng.rt.MaxInFlight)
	}
}

func TestPollNetworkStatusFallback(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "loadgen.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const minedTx = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	liveAccepted(t, st, "p-mined", minedTx)
	watch := &fakeWatcher{query: []arcadestatus.Event{{TxID: minedTx, Status: "MINED"}}}
	eng := testEngineWithStore(t, st, watch)
	if err := eng.pollNetworkStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, err := st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.InFlight != 0 || c.Seen != 1 || c.Confirmed != 0 {
		t.Fatalf("MINED should mark seen, not confirmed: %+v", c)
	}
	p, ok, err := st.Get("p-mined")
	if err != nil || !ok || p.State != store.StateSeen {
		t.Fatalf("state %+v ok=%v err=%v", p, ok, err)
	}
}

func TestPollNetworkStatusWhatsOnChainFallback(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "loadgen.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const minedTx = "eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	const otherTx = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
	liveAccepted(t, st, "p-arcade-miss", minedTx)
	liveAccepted(t, st, "p-also-miss", otherTx)
	// Arcade knows nothing (404s / empty) and even returns a transport error,
	// which is what happened when the four in-flight slots filled up.
	watch := &fakeWatcher{err: context.DeadlineExceeded}
	eng := testEngineWithStore(t, st, watch)
	var asked []string
	eng.wocQuery = func(_ context.Context, network string, txids []string) ([]arcadestatus.Event, error) {
		asked = append([]string{}, txids...)
		if network == "" {
			t.Fatal("expected a network")
		}
		return []arcadestatus.Event{{TxID: minedTx, Status: "MINED"}, {TxID: otherTx, Status: "SEEN_ON_NETWORK"}}, nil
	}
	if err := eng.pollNetworkStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	if len(asked) != 2 {
		t.Fatalf("woc queried %v", asked)
	}
	c, err := st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.InFlight != 0 || c.Seen != 2 {
		t.Fatalf("WoC fallback should free both slots: %+v", c)
	}
}

func TestPollNetworkStatusSkipsWhatsOnChainWhenArcadeFrees(t *testing.T) {
	st, err := store.Open(filepath.Join(t.TempDir(), "loadgen.sqlite"), true)
	if err != nil {
		t.Fatal(err)
	}
	defer st.Close()
	const minedTx = "abababababababababababababababababababababababababababababababab"
	liveAccepted(t, st, "p-arcade", minedTx)
	watch := &fakeWatcher{query: []arcadestatus.Event{{TxID: minedTx, Status: "SEEN_ON_NETWORK"}}}
	eng := testEngineWithStore(t, st, watch)
	eng.wocQuery = func(context.Context, string, []string) ([]arcadestatus.Event, error) {
		t.Fatal("should not ask WhatsOnChain when Arcade already freed the slot")
		return nil, nil
	}
	if err := eng.pollNetworkStatus(context.Background()); err != nil {
		t.Fatal(err)
	}
	c, err := st.Counts()
	if err != nil {
		t.Fatal(err)
	}
	if c.InFlight != 0 || c.Seen != 1 {
		t.Fatalf("counts %+v", c)
	}
}

