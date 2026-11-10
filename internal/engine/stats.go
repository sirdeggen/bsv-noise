package engine

import (
	"fmt"
	"strings"
	"time"

	"github.com/sirdeggen/bsv-noise/internal/httpserver"
)

// Stats is the read-only document behind /api/stats. It contains the public
// identity key and no private key material.
func (e *Engine) Stats() (httpserver.Stats, error) {
	now := e.clock.Now()
	e.refresh(now)
	recent, err := e.st.RecentPayments(12)
	if err != nil {
		return httpserver.Stats{}, err
	}
	dry, broadcast, err := e.st.AcceptedKinds()
	if err != nil {
		return httpserver.Stats{}, err
	}
	_, variance, gaps := e.attemptW.IntervalStats(now)

	e.mu.Lock()
	defer e.mu.Unlock()
	var sweepAt *time.Time
	if !e.lastSweep.IsZero() {
		t := e.lastSweep.UTC()
		sweepAt = &t
	}
	targetVar := 0.0
	if e.rt.TargetTPS > 0 {
		mean := 1 / e.rt.TargetTPS
		targetVar = mean * mean
	}
	state := "tracking"
	switch {
	case e.status.Paused == "backpressure":
		state = "backpressure"
	case e.status.Paused != "":
		state = "paused"
	case gaps < 2 && e.snap.AcceptedRate == 0:
		state = "idle"
	}
	out := httpserver.Stats{
		Synthetic:       true,
		Health:          "ok",
		Ready:           e.status.Ready,
		Paused:          e.status.Paused,
		Mode:            e.status.Mode,
		Network:         e.status.Network,
		DryRun:          !e.rt.Broadcast,
		Broadcast:       e.rt.Broadcast,
		ImportFunding:   e.rt.ImportFunding,
		IdentityKey:     e.status.IdentityKey,
		MessageBox:      e.status.MessageBox,
		MessageBoxURL:   e.status.MessageBoxURL,
		Originator:      e.rt.Originator,
		BalanceSatoshis: e.snap.Balance,
		BalanceBSV:      formatBSV(e.snap.Balance),
		Transactions: httpserver.TransactionCounts{
			Total:     dry + broadcast,
			Broadcast: broadcast,
			DryRun:    dry,
			Confirmed: e.snap.Confirmed,
			Seen:      e.snap.Seen,
			Failed:    e.snap.Failed,
			InFlight:  e.snap.InFlight,
			DustSwept: e.dustSwept,
		},
		TPS: httpserver.TPS{
			Target:                      e.rt.TargetTPS,
			Attempted:                   e.snap.AttemptedRate,
			Accepted:                    e.snap.AcceptedRate,
			Confirmed:                   e.snap.ConfirmedRate,
			Failed:                      e.snap.FailedRate,
			IntervalVarianceSeconds2:    variance,
			ExponentialVarianceSeconds2: targetVar,
			SampleGaps:                  gaps,
			State:                       state,
		},
		Funding: httpserver.FundingView{
			Pending:      e.snap.SweepPending,
			Acknowledged: e.snap.SweepAcked,
			Failed:       e.snap.SweepFailed,
			LastSweep:    sweepAt,
			LastError:    e.lastSweepErr,
		},
		Note: e.status.Note,
	}
	for _, p := range recent {
		out.Recent = append(out.Recent, httpserver.RecentTx{
			ID:          p.ID,
			TxID:        p.TxID,
			Amount:      p.Amount,
			Fee:         p.Fee,
			State:       p.State,
			DryRun:      p.DryRun,
			ExplorerURL: explorerURL(e.status.Network, p.TxID),
			Created:     p.Created,
		})
	}
	if out.Recent == nil {
		out.Recent = []httpserver.RecentTx{}
	}
	return out, nil
}

func formatBSV(sats int64) string {
	sign := ""
	if sats < 0 {
		sign = "-"
		sats = -sats
	}
	return fmt.Sprintf("%s%d.%08d", sign, sats/100_000_000, sats%100_000_000)
}

func explorerURL(network, txid string) string {
	if txid == "" || strings.HasPrefix(txid, "sim:") {
		return ""
	}
	if network == "main" {
		return "https://whatsonchain.com/tx/" + txid
	}
	return "https://test.whatsonchain.com/tx/" + txid
}
