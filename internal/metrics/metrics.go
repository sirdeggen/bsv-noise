// Package metrics exposes a small Prometheus text snapshot. It has no admin
// surface and does not include private keys.
package metrics

import (
	"fmt"
	"strings"
	"sync"
	"time"
)

// Snapshot is a point-in-time view the HTTP handler renders.
type Snapshot struct {
	Mode          string
	Network       string
	Synthetic     bool
	Paused        string
	Attempted     int64
	Accepted      int64
	Confirmed     int64
	Seen          int64
	Failed        int64
	InFlight      int64
	InDoubt       int64
	Fees          int64
	Spend         int64
	Balance       int64
	SweepImported int64
	SweepAcked    int64
	SweepPending  int64
	SweepFailed   int64
	AttemptedRate float64
	AcceptedRate  float64
	ConfirmedRate float64
	FailedRate    float64
	RSSBytes      int64
}

// Window tracks event times for a short rate.
type Window struct {
	mu     sync.Mutex
	stamps []time.Time
	keep   time.Duration
}

func NewWindow(keep time.Duration) *Window {
	return &Window{keep: keep}
}

func (w *Window) Add(now time.Time) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.stamps = append(w.stamps, now)
	w.trim(now)
}

func (w *Window) Rate(now time.Time) float64 {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.trim(now)
	if w.keep <= 0 {
		return 0
	}
	return float64(len(w.stamps)) / w.keep.Seconds()
}

// IntervalStats reports the sample mean and variance of gaps between events
// still inside the window. Variance is in seconds squared. Fewer than two gaps
// returns zeros.
func (w *Window) IntervalStats(now time.Time) (mean, variance float64, gaps int) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.trim(now)
	if len(w.stamps) < 3 {
		return 0, 0, 0
	}
	deltas := make([]float64, 0, len(w.stamps)-1)
	for i := 1; i < len(w.stamps); i++ {
		deltas = append(deltas, w.stamps[i].Sub(w.stamps[i-1]).Seconds())
	}
	var sum float64
	for _, d := range deltas {
		sum += d
	}
	mean = sum / float64(len(deltas))
	var ss float64
	for _, d := range deltas {
		diff := d - mean
		ss += diff * diff
	}
	if len(deltas) > 1 {
		variance = ss / float64(len(deltas)-1)
	}
	return mean, variance, len(deltas)
}

func (w *Window) trim(now time.Time) {
	cut := now.Add(-w.keep)
	i := 0
	for i < len(w.stamps) && w.stamps[i].Before(cut) {
		i++
	}
	w.stamps = w.stamps[i:]
}

// Text renders Prometheus exposition. Label values are restricted to a safe set.
func Text(s Snapshot) string {
	var b strings.Builder
	g := func(name, help string, v any) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s gauge\n%s %v\n", name, help, name, name, v)
	}
	c := func(name, help string, v int64) {
		fmt.Fprintf(&b, "# HELP %s %s\n# TYPE %s counter\n%s %d\n", name, help, name, name, v)
	}
	mode := safe(s.Mode)
	net := safe(s.Network)
	fmt.Fprintf(&b, "# HELP brc29_load_info synthetic load generator build info\n# TYPE brc29_load_info gauge\nbrc29_load_info{mode=%q,network=%q,synthetic=\"true\"} 1\n", mode, net)
	c("brc29_load_payments_attempted_total", "Payment attempts that reserved inputs or called the wallet.", s.Attempted)
	c("brc29_load_payments_accepted_total", "Payments the wallet or dry-run ledger accepted. Not confirmations.", s.Accepted)
	c("brc29_load_payments_confirmed_total", "Payments mined with a merkle path. Seen-on-network is not confirmation.", s.Confirmed)
	c("brc29_load_payments_seen_total", "Payments Arcade has reported seen on the network, including later confirmed ones.", s.Seen)
	c("brc29_load_payments_failed_total", "Payments that failed without acceptance.", s.Failed)
	g("brc29_load_payments_inflight", "Accepted payments not yet seen on the network, plus in-doubt attempts.", s.InFlight)
	g("brc29_load_payments_in_doubt", "Attempts whose broadcast outcome is unknown after a crash or timeout.", s.InDoubt)
	g("brc29_load_fees_satoshis", "Satoshis accounted as fees on accepted payments and maintenance.", s.Fees)
	g("brc29_load_spend_satoshis", "Satoshis paid to controlled recipients on accepted payments.", s.Spend)
	g("brc29_load_balance_satoshis", "Spendable balance. Dry-run is the simulated ledger.", s.Balance)
	g("brc29_load_sweep_imported", "Message Box payments internalized in this process lifetime snapshot.", s.SweepImported)
	g("brc29_load_sweep_acknowledged", "Message Box payments acknowledged.", s.SweepAcked)
	g("brc29_load_sweep_pending", "Imported payments whose acknowledge has not completed.", s.SweepPending)
	g("brc29_load_sweep_failed", "Message Box payments the wallet did not accept.", s.SweepFailed)
	g("brc29_load_tps_attempted", "Attempted payments per second over the recent window.", s.AttemptedRate)
	g("brc29_load_tps_accepted", "Accepted payments per second over the recent window.", s.AcceptedRate)
	g("brc29_load_tps_confirmed", "Confirmed payments per second over the recent window.", s.ConfirmedRate)
	g("brc29_load_tps_failed", "Failed payments per second over the recent window.", s.FailedRate)
	g("brc29_load_process_rss_bytes", "Resident set size when /proc is available.", s.RSSBytes)
	if s.Paused != "" {
		fmt.Fprintf(&b, "brc29_load_paused{reason=%q} 1\n", safe(s.Paused))
	} else {
		b.WriteString("brc29_load_paused{reason=\"none\"} 0\n")
	}
	return b.String()
}

func safe(s string) string {
	s = strings.Map(func(r rune) rune {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '_', r == '-':
			return r
		default:
			return '_'
		}
	}, strings.ToLower(s))
	if s == "" {
		return "unknown"
	}
	return s
}
