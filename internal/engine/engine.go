// Package engine runs the synthetic payment schedule and the funding sweep.
//
// The schedule spaces events by sampled intervals. Time spent blocked on
// in-flight payments or on an empty wallet does not produce a catch-up burst.
// Dry-run spends a simulated ledger. Broadcast and import use the live wallet.
package engine

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"sync"
	"time"

	"github.com/sirdeggen/bsv-noise/internal/arcadestatus"
	"github.com/sirdeggen/bsv-noise/internal/brc29pay"
	"github.com/sirdeggen/bsv-noise/internal/config"
	"github.com/sirdeggen/bsv-noise/internal/fee"
	"github.com/sirdeggen/bsv-noise/internal/funding"
	"github.com/sirdeggen/bsv-noise/internal/httpserver"
	"github.com/sirdeggen/bsv-noise/internal/live"
	"github.com/sirdeggen/bsv-noise/internal/messagebox"
	"github.com/sirdeggen/bsv-noise/internal/metrics"
	"github.com/sirdeggen/bsv-noise/internal/peerpay"
	"github.com/sirdeggen/bsv-noise/internal/schedule"
	"github.com/sirdeggen/bsv-noise/internal/store"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
)

// Engine is the process loop.
type Engine struct {
	rt         config.Runtime
	st         *store.Store
	sampler    *schedule.Sampler
	clock      schedule.Clock
	pacer      *schedule.Pacer
	recipients []*ec.PublicKey
	box        *messagebox.Client
	live       *live.Sender
	watch      NetworkWatcher
	retry      *rebroadcastTracker
	wocQuery   func(context.Context, string, []string) ([]arcadestatus.Event, error)
	log        *slog.Logger
	poll       time.Duration

	mu           sync.Mutex
	status       httpserver.Status
	snap         metrics.Snapshot
	attemptW     *metrics.Window
	acceptW      *metrics.Window
	confirmW     *metrics.Window
	failW        *metrics.Window
	sweep        funding.Result
	lastSweep    time.Time
	lastSweepErr string
	dustSwept    int
}

// Options configures an engine. Clock nil uses the real clock.
type Options struct {
	Runtime    config.Runtime
	Store      *store.Store
	Recipients []*ec.PublicKey
	Box        *messagebox.Client
	Live       *live.Sender
	Watcher    NetworkWatcher
	Log        *slog.Logger
	Clock      schedule.Clock
	Poll       time.Duration
}

func New(opt Options) (*Engine, error) {
	if opt.Store == nil {
		return nil, errors.New("store is required")
	}
	if len(opt.Recipients) == 0 {
		return nil, errors.New("at least one controlled recipient is required")
	}
	clock := opt.Clock
	if clock == nil {
		clock = schedule.RealClock{}
	}
	seed := opt.Runtime.Seed
	if seed == 0 {
		seed = time.Now().UnixNano()
	}
	sampler := schedule.NewSampler(seed, opt.Runtime.TargetTPS, opt.Runtime.MinAmount, opt.Runtime.MaxAmount, opt.Runtime.Distribution)
	log := opt.Log
	if log == nil {
		log = slog.Default()
	}
	poll := opt.Poll
	if poll <= 0 {
		poll = 50 * time.Millisecond
	}
	mode := "dry-run"
	if opt.Runtime.Broadcast {
		mode = "broadcast"
	} else if opt.Runtime.ImportFunding {
		mode = "import-only"
	}
	e := &Engine{
		rt:      opt.Runtime,
		st:      opt.Store,
		sampler: sampler,
		clock:   clock,
		pacer: schedule.NewPacer(func() time.Duration {
			return sampler.IntervalAt(clock.Now())
		}, clock),
		recipients: opt.Recipients,
		box:        opt.Box,
		live:       opt.Live,
		watch:      opt.Watcher,
		retry:      &rebroadcastTracker{last: map[string]time.Time{}},
		log:        log,
		poll:       poll,
		attemptW:   metrics.NewWindow(10 * time.Second),
		acceptW:    metrics.NewWindow(10 * time.Second),
		confirmW:   metrics.NewWindow(10 * time.Second),
		failW:      metrics.NewWindow(10 * time.Second),
	}
	e.status = httpserver.Status{
		Synthetic:     true,
		Mode:          mode,
		Network:       opt.Runtime.Network,
		IdentityKey:   opt.Runtime.Identity,
		MessageBox:    opt.Runtime.MessageBox,
		MessageBoxURL: opt.Runtime.MessageBoxURL,
		Broadcast:     opt.Runtime.Broadcast,
		ImportFunding: opt.Runtime.ImportFunding,
		Ready:         true,
		Note:          "A trickle of ordinary-looking payments so real commerce is a needle in a haystack.",
		Toolbox:       "github.com/bsv-blockchain/go-wallet-toolbox v0.191.4",
		GoSDK:         "github.com/bsv-blockchain/go-sdk v1.7.3",
	}
	e.snap.Mode = mode
	e.snap.Network = opt.Runtime.Network
	e.snap.Synthetic = true
	return e, nil
}

func (e *Engine) Status() httpserver.Status {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.status
}

func (e *Engine) Metrics() string {
	e.refresh(e.clock.Now())
	e.mu.Lock()
	defer e.mu.Unlock()
	return metrics.Text(e.snap)
}

// Run blocks until the context is cancelled or max_payments accepted payments
// have been produced.
func (e *Engine) Run(ctx context.Context) error {
	errCh := make(chan error, 4)
	var wg sync.WaitGroup
	if e.rt.ImportFunding && e.box != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- e.sweepLoop(ctx)
		}()
	}
	if e.rt.Broadcast && e.watch != nil {
		wg.Add(1)
		go func() {
			defer wg.Done()
			errCh <- e.seenLoop(ctx)
		}()
	}
	wg.Add(2)
	go func() {
		defer wg.Done()
		errCh <- e.confirmLoop(ctx)
	}()
	go func() {
		defer wg.Done()
		errCh <- e.generateLoop(ctx)
	}()
	wg.Wait()
	close(errCh)
	for err := range errCh {
		if err != nil && !errors.Is(err, context.Canceled) {
			return err
		}
	}
	return nil
}

func (e *Engine) generateLoop(ctx context.Context) error {
	if e.rt.ImportFunding && !e.rt.Broadcast {
		e.log.Info("import-only mode does not generate payments")
		<-ctx.Done()
		return ctx.Err()
	}
	accepted := 0
	for {
		if err := ctx.Err(); err != nil {
			return err
		}
		if e.rt.MaxPayments > 0 && accepted >= e.rt.MaxPayments {
			return nil
		}
		blocked, err := e.waitForSlot(ctx)
		if err != nil {
			return err
		}
		if !blocked {
			if err := e.pacer.Wait(ctx); err != nil {
				return err
			}
		}
		if e.rt.Broadcast {
			if err := e.maintainLive(ctx); err != nil {
				e.log.Warn("utxo maintenance skipped", "error", err.Error())
			}
		} else {
			e.maintainSim()
		}
		ok, err := e.one(ctx)
		if err != nil {
			return err
		}
		if ok {
			accepted++
		}
	}
}

func (e *Engine) waitForSlot(ctx context.Context) (bool, error) {
	blocked := false
	for {
		c, err := e.st.Counts()
		if err != nil {
			return false, err
		}
		if c.InDoubt > 0 {
			e.pause("in_doubt")
			return false, fmt.Errorf("in-doubt payment: refusing to send until an operator reviews the wallet")
		}
		if int(c.InFlight) < e.rt.MaxInFlight {
			if blocked {
				e.pause("")
			}
			return blocked, nil
		}
		blocked = true
		e.pause("backpressure")
		if err := e.clock.Sleep(ctx, e.poll); err != nil {
			return false, err
		}
	}
}

func (e *Engine) one(ctx context.Context) (bool, error) {
	now := e.clock.Now()
	est := fee.Standard(e.rt.SatPerKB)
	if est < e.rt.MinFeeSat || est > e.rt.MaxFeeSat {
		e.pause("fee_bound")
		return false, nil
	}
	amount := e.sampler.Amount()
	recipient := e.recipients[e.sampler.Index(len(e.recipients))]
	out, err := brc29pay.Build(e.rt.Private, recipient, e.rt.Mainnet)
	if err != nil {
		return false, err
	}
	id, err := e.st.NextID()
	if err != nil {
		return false, err
	}
	req := store.BeginRequest{
		ID: id, Amount: amount, Fee: est, Recipient: recipient.ToDERHex(),
		Prefix: out.Prefix, Suffix: out.Suffix, LockingScript: fmt.Sprintf("%x", out.LockingScript),
		Now: now, Reserve: e.rt.ReserveSat, DailySpendCap: e.rt.DailySpendCap,
		DailyFeeCap: e.rt.DailyFeeCap, MaxInputs: e.rt.MaxInputs,
	}
	if e.rt.Broadcast {
		bal, err := e.live.Wallet.Balance(ctx)
		if err != nil {
			e.fail("balance", err)
			return false, nil
		}
		req.Live = true
		req.Available = int64(bal)
	}
	if _, err := e.st.Begin(req); err != nil {
		switch {
		case errors.Is(err, store.ErrInsufficientFunds):
			e.pause("insufficient_funds")
		case errors.Is(err, store.ErrDailyCap):
			e.pause("daily_cap")
		case errors.Is(err, store.ErrInDoubt):
			e.pause("in_doubt")
		default:
			e.fail("begin", err)
		}
		_ = e.clock.Sleep(ctx, e.poll)
		return false, nil
	}
	e.markAttempt(now)
	if !e.rt.Broadcast {
		txid := "sim:" + id
		if _, err := e.st.FinishAccepted(id, txid, 0, now); err != nil {
			_ = e.st.Abort(id, "dry-run finish failed")
			e.fail("finish", err)
			return false, nil
		}
		e.markAccept(now)
		e.pause("")
		return true, nil
	}
	outcome, err := e.live.Send(ctx, id, out, uint64(amount))
	if outcome.InDoubt {
		e.log.Error("wallet call outcome is unknown; stopping new sends", "payment", id)
		e.pause("in_doubt")
		return false, fmt.Errorf("in-doubt wallet call for %s", id)
	}
	if err != nil || !outcome.Accepted {
		reason := "broadcast not accepted"
		if err != nil {
			reason = err.Error()
		}
		_ = e.st.Fail(id, truncate(reason, 200))
		e.fail("broadcast", err)
		return false, nil
	}
	if _, err := e.st.FinishAccepted(id, outcome.TxID, 0, now); err != nil {
		e.log.Error("accepted broadcast could not be recorded", "payment", id)
		e.pause("in_doubt")
		return false, err
	}
	e.markAccept(now)
	e.pause("")
	return true, nil
}

func (e *Engine) maintainSim() {
	now := e.clock.Now()
	day := now.UTC().Format("2006-01-02")
	free, _, err := e.st.UTXOCount()
	if err != nil {
		return
	}
	feeSat := e.rt.StandardFee
	if free > e.rt.MaxUTXOs {
		_, _ = e.st.Consolidate(e.rt.MaxUTXOs, feeSat, day, e.rt.DailyFeeCap)
		return
	}
	if free < e.rt.MinUTXOs {
		_, _ = e.st.Split(e.rt.MinUTXOs, feeSat, day, e.rt.DailyFeeCap)
	}
}

func (e *Engine) maintainLive(ctx context.Context) error {
	if e.live == nil {
		return nil
	}
	n, err := e.live.Maintain(ctx, e.rt.MinUTXOs, e.rt.MaxUTXOs)
	if n > 0 {
		e.mu.Lock()
		e.dustSwept += n
		e.mu.Unlock()
		e.log.Info("swept dust outputs", "inputs", n, "total_swept_inputs", e.dustSwept)
	}
	return err
}

func (e *Engine) confirmLoop(ctx context.Context) error {
	for {
		if err := e.clock.Sleep(ctx, e.rt.ConfirmEvery); err != nil {
			return err
		}
		n, err := e.confirm(ctx)
		if err != nil {
			e.log.Warn("confirm poll", "error", err.Error())
			continue
		}
		if n > 0 {
			now := e.clock.Now()
			for i := 0; i < n; i++ {
				e.confirmW.Add(now)
			}
		}
	}
}

func (e *Engine) confirm(ctx context.Context) (int, error) {
	if !e.rt.Broadcast {
		return e.st.ConfirmDue(e.clock.Now(), e.rt.ConfirmEvery)
	}
	pays, err := e.st.UnconfirmedLive()
	if err != nil {
		return 0, err
	}
	var txids []string
	for _, p := range pays {
		if p.TxID != "" {
			txids = append(txids, p.TxID)
		}
	}
	completed, err := e.live.Wallet.CompletedTxIDs(ctx, txids)
	if err != nil {
		return 0, err
	}
	n := 0
	for _, p := range pays {
		if !completed[p.TxID] {
			continue
		}
		ok, err := e.st.ConfirmID(p.ID, e.clock.Now())
		if err != nil {
			return n, err
		}
		if ok {
			n++
		}
	}
	e.log.Info("confirm poll", "unconfirmed", len(pays), "already_completed_in_wallet", len(completed), "marked", n)
	return n, nil
}

func (e *Engine) sweepLoop(ctx context.Context) error {
	// One immediate pass, then the interval. A quiet box does not burst.
	e.sweepOnce(ctx)
	for {
		if err := e.clock.Sleep(ctx, e.rt.SweepEvery); err != nil {
			return err
		}
		e.sweepOnce(ctx)
	}
}

func (e *Engine) sweepOnce(ctx context.Context) {
	if e.live == nil {
		return
	}
	res, err := funding.SweepOnce(ctx, e.box, e.st, liveImporter{s: e.live}, false)
	if err != nil {
		e.mu.Lock()
		e.lastSweep = e.clock.Now()
		e.lastSweepErr = err.Error()
		e.mu.Unlock()
		e.log.Warn("funding sweep", "error", err.Error())
		return
	}
	e.mu.Lock()
	e.sweep = res
	e.lastSweep = e.clock.Now()
	e.lastSweepErr = ""
	e.mu.Unlock()
	e.log.Info("funding sweep", "listed", res.Listed, "imported", res.Imported, "acked", res.Acked, "failed", res.Failed, "malformed", res.Malformed)
}

type liveImporter struct{ s *live.Sender }

func (l liveImporter) Internalize(ctx context.Context, in peerpay.Incoming) (bool, error) {
	return l.s.Internalize(ctx, in)
}

func (e *Engine) pause(reason string) {
	e.mu.Lock()
	e.status.Paused = reason
	e.status.Ready = reason == "" || reason == "backpressure"
	e.snap.Paused = reason
	e.mu.Unlock()
}

func (e *Engine) markAttempt(now time.Time) {
	e.attemptW.Add(now)
}

func (e *Engine) markAccept(now time.Time) {
	e.acceptW.Add(now)
}

func (e *Engine) fail(where string, err error) {
	e.failW.Add(e.clock.Now())
	if err != nil {
		e.log.Warn("payment failed", "where", where, "error", truncate(err.Error(), 200))
	}
}

func (e *Engine) refresh(now time.Time) {
	c, err := e.st.Counts()
	bal, _, balErr := e.st.Balance()
	if e.rt.Broadcast && e.live != nil {
		if liveBal, lerr := e.live.Wallet.Balance(context.Background()); lerr == nil {
			bal = int64(liveBal)
			balErr = nil
		}
	}
	rss := readRSS()
	e.mu.Lock()
	defer e.mu.Unlock()
	if err == nil {
		e.snap.Attempted = c.Attempted
		e.snap.Accepted = c.Accepted
		e.snap.Confirmed = c.Confirmed
		e.snap.Seen = c.Seen
		e.snap.Failed = c.Failed
		e.snap.InFlight = c.InFlight
		e.snap.InDoubt = c.InDoubt
		e.snap.Fees = c.Fees
		e.snap.Spend = c.Spend
		e.snap.SweepAcked = c.FundAck
		e.snap.SweepPending = c.FundPend
		e.snap.SweepFailed = c.FundFail
		e.snap.SweepImported = c.FundAck + c.FundPend
	}
	if balErr == nil {
		e.snap.Balance = bal
	}
	e.snap.AttemptedRate = e.attemptW.Rate(now)
	e.snap.AcceptedRate = e.acceptW.Rate(now)
	e.snap.ConfirmedRate = e.confirmW.Rate(now)
	e.snap.FailedRate = e.failW.Rate(now)
	e.snap.RSSBytes = rss
	e.snap.Paused = e.status.Paused
	e.snap.Mode = e.status.Mode
	e.snap.Network = e.status.Network
}

func readRSS() int64 {
	buf, err := os.ReadFile("/proc/self/statm")
	if err != nil {
		return 0
	}
	var size, rss int64
	if _, err := fmt.Sscan(string(buf), &size, &rss); err != nil {
		return 0
	}
	return rss * int64(os.Getpagesize())
}

func truncate(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n]
}
