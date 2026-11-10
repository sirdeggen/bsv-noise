// Package live drives go-wallet-toolbox for broadcast and import.
// Acceptance is a CreateAction or InternalizeAction result. Confirmation is a
// later listActions status of "completed". A transport error after the call
// starts is treated as in doubt rather than retried.
package live

import (
	"context"
	"errors"
	"strings"
	"time"

	sdk "github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/sirdeggen/bsv-noise/internal/brc29pay"
	"github.com/sirdeggen/bsv-noise/internal/peerpay"
)

// Wallet is the subset of the toolbox wallet this process calls.
type Wallet interface {
	Balance(ctx context.Context) (uint64, error)
	CreateAction(ctx context.Context, args sdk.CreateActionArgs, originator string) (*sdk.CreateActionResult, error)
	InternalizeAction(ctx context.Context, args sdk.InternalizeActionArgs, originator string) (*sdk.InternalizeActionResult, error)
	ListOutputs(ctx context.Context, args sdk.ListOutputsArgs, originator string) (*sdk.ListOutputsResult, error)
	ListActions(ctx context.Context, args sdk.ListActionsArgs, originator string) (*sdk.ListActionsResult, error)
	PrepareBasket(ctx context.Context, targetChange int, minDesired uint64) error
	SweepDust(ctx context.Context, threshold uint64, satPerKB int64) (int, error)
	CompletedTxIDs(ctx context.Context, txids []string) (map[string]bool, error)
}

// Fuel is the optional throughput fan-out. Privacy wallets do not implement a
// successful fan-out; the caller must not invoke it unless the storage
// strategy is throughput.
type Fuel interface {
	FanOutFuel(ctx context.Context, count int, each uint64) error
}

// Sender serializes nothing by itself. The wallet implementation holds the lock
// shared with funding.
type Sender struct {
	Wallet         Wallet
	Fuel           Fuel
	Originator     string
	Throughput     bool
	TargetChange   int
	MinDesiredUTXO uint64
	DustSat        uint64
	SatPerKB       int64
	lastRemix      time.Time
}

// Outcome is one broadcast attempt.
type Outcome struct {
	TxID     string
	Accepted bool
	InDoubt  bool
}

// Send pays one controlled recipient. The description stored by the wallet is
// explicitly a synthetic load-test payment. Nothing in the call fabricates a
// receipt or a merchant invoice.
func (s *Sender) Send(ctx context.Context, id string, out brc29pay.Output, amount uint64) (Outcome, error) {
	falseVal := false
	trueVal := true
	args := sdk.CreateActionArgs{
		Description: "synthetic BRC-29 load-test payment",
		Outputs: []sdk.CreateActionOutput{{
			LockingScript:      out.LockingScript,
			Satoshis:           amount,
			OutputDescription:  "synthetic load-test payment to a controlled recipient",
			CustomInstructions: out.Instructions,
			Tags:               []string{"synthetic-load"},
		}},
		Labels: []string{"synthetic-load", id},
		Options: &sdk.CreateActionOptions{
			RandomizeOutputs:       &trueVal,
			AcceptDelayedBroadcast: &falseVal,
		},
	}
	res, err := s.Wallet.CreateAction(ctx, args, s.Originator)
	if err != nil {
		if ambiguous(ctx, err) {
			return Outcome{InDoubt: true}, err
		}
		return Outcome{}, err
	}
	if res == nil || res.Txid.String() == "" || res.Txid.String() == strings.Repeat("0", 64) {
		return Outcome{}, errors.New("wallet returned no txid")
	}
	for _, sw := range res.SendWithResults {
		if sw.Status == sdk.ActionResultStatusFailed {
			return Outcome{TxID: res.Txid.String()}, errors.New("broadcast status failed")
		}
	}
	return Outcome{TxID: res.Txid.String(), Accepted: true}, nil
}

// Internalize submits a Message Box payment to the wallet. Accepted means the
// wallet took the output. It does not mean the payment is confirmed, and the
// SDK result does not say whether an unproven transaction was rebroadcast.
func (s *Sender) Internalize(ctx context.Context, in peerpay.Incoming) (bool, error) {
	res, err := s.Wallet.InternalizeAction(ctx, in.InternalizeArgs("incoming BRC-29 message box payment"), s.Originator)
	if err != nil {
		return false, err
	}
	return res != nil && res.Accepted, nil
}

// Confirmed returns ids whose wallet action status is completed.
func (s *Sender) Confirmed(ctx context.Context, ids []string) ([]string, error) {
	var done []string
	limit := uint32(5)
	include := true
	for _, id := range ids {
		res, err := s.Wallet.ListActions(ctx, sdk.ListActionsArgs{
			Labels:         []string{id},
			LabelQueryMode: sdk.QueryModeAll,
			IncludeLabels:  &include,
			Limit:          &limit,
		}, s.Originator)
		if err != nil {
			return done, err
		}
		if res == nil {
			continue
		}
		for _, act := range res.Actions {
			if act.Status == sdk.ActionStatusCompleted {
				done = append(done, id)
				break
			}
		}
	}
	return done, nil
}

// Maintain remixes change when the default basket is above maxUTXOs. Throughput
// mode also asks the wallet for one fan-out when the basket is below minUTXOs.
// Either call is one transaction, not a catch-up loop.
func (s *Sender) Maintain(ctx context.Context, minUTXO, maxUTXO int) (dustSwept int, err error) {
	if err := s.Wallet.PrepareBasket(ctx, s.TargetChange, s.MinDesiredUTXO); err != nil {
		return 0, err
	}
	if s.DustSat > 0 && s.SatPerKB > 0 {
		n, sweepErr := s.Wallet.SweepDust(ctx, s.DustSat, s.SatPerKB)
		if sweepErr != nil {
			// Sweep failures must not block payments or hold the path
			// that contends with toolbox SQLite writers.
			dustSwept = 0
		} else {
			dustSwept = n
		}
	}
	limit := uint32(1)
	res, err := s.Wallet.ListOutputs(ctx, sdk.ListOutputsArgs{Basket: "default", Limit: &limit}, s.Originator)
	if err != nil {
		return dustSwept, err
	}
	total := 0
	if res != nil {
		total = int(res.TotalOutputs)
		if total == 0 {
			total = len(res.Outputs)
		}
	}
	if maxUTXO > 0 && total > maxUTXO && (s.lastRemix.IsZero() || time.Since(s.lastRemix) >= 3*time.Minute) {
		_, err := s.Wallet.CreateAction(ctx, sdk.CreateActionArgs{Description: "synthetic change remix"}, s.Originator)
		s.lastRemix = time.Now()
		return dustSwept, err
	}
	if s.Throughput && s.Fuel != nil && total < minUTXO {
		return dustSwept, s.Fuel.FanOutFuel(ctx, minUTXO, 2000)
	}
	return dustSwept, nil
}

func ambiguous(ctx context.Context, err error) bool {
	if ctx.Err() != nil || errors.Is(err, context.DeadlineExceeded) || errors.Is(err, context.Canceled) {
		return true
	}
	msg := strings.ToLower(err.Error())
	return strings.Contains(msg, "timeout") || strings.Contains(msg, "connection refused") || strings.Contains(msg, "temporarily")
}
