package live

import (
	"context"
	"errors"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/sirdeggen/bsv-noise/internal/brc29pay"
)

type stubWallet struct {
	byLabel map[string]sdk.ActionStatus
	err     error
	last    *sdk.CreateActionArgs
}

func (s *stubWallet) PrepareBasket(context.Context, int, uint64) error { return nil }
func (s *stubWallet) SweepDust(context.Context, uint64, int64) (int, error) {
	return 0, nil
}
func (s *stubWallet) CompletedTxIDs(context.Context, []string) (map[string]bool, error) {
	return map[string]bool{}, nil
}

func (s *stubWallet) Balance(context.Context) (uint64, error) { return 0, nil }
func (s *stubWallet) CreateAction(_ context.Context, args sdk.CreateActionArgs, _ string) (*sdk.CreateActionResult, error) {
	cp := args
	s.last = &cp
	var txid chainhash.Hash
	txid[0] = 1
	return &sdk.CreateActionResult{Txid: txid}, nil
}
func (s *stubWallet) InternalizeAction(context.Context, sdk.InternalizeActionArgs, string) (*sdk.InternalizeActionResult, error) {
	return nil, nil
}
func (s *stubWallet) ListOutputs(context.Context, sdk.ListOutputsArgs, string) (*sdk.ListOutputsResult, error) {
	return nil, nil
}
func (s *stubWallet) ListActions(_ context.Context, args sdk.ListActionsArgs, _ string) (*sdk.ListActionsResult, error) {
	if s.err != nil {
		return nil, s.err
	}
	res := &sdk.ListActionsResult{}
	for _, label := range args.Labels {
		if st, ok := s.byLabel[label]; ok {
			res.Actions = append(res.Actions, sdk.Action{Status: st})
		}
	}
	return res, nil
}

func TestConfirmedRequiresCompletedStatus(t *testing.T) {
	s := &Sender{Wallet: &stubWallet{byLabel: map[string]sdk.ActionStatus{
		"p-1": sdk.ActionStatusUnproven,
		"p-2": sdk.ActionStatusCompleted,
		"p-3": sdk.ActionStatusSending,
	}}}
	done, err := s.Confirmed(context.Background(), []string{"p-1", "p-2", "p-3", "p-missing"})
	if err != nil {
		t.Fatal(err)
	}
	if len(done) != 1 || done[0] != "p-2" {
		t.Fatalf("completed %v", done)
	}
}

func TestSendRandomizesOutputs(t *testing.T) {
	w := &stubWallet{}
	s := &Sender{Wallet: w, Originator: "test.local"}
	out, err := s.Send(context.Background(), "p-9", brc29pay.Output{LockingScript: []byte{0x51}, Instructions: "{}"}, 150)
	if err != nil {
		t.Fatal(err)
	}
	if !out.Accepted {
		t.Fatal("expected accepted")
	}
	if w.last == nil || w.last.Options == nil || w.last.Options.RandomizeOutputs == nil || !*w.last.Options.RandomizeOutputs {
		t.Fatalf("RandomizeOutputs should be true, got %+v", w.last)
	}
}

type errSweepWallet struct{ stubWallet }

func (s *errSweepWallet) SweepDust(context.Context, uint64, int64) (int, error) {
	return 0, errors.New("database is locked")
}

func TestMaintainIgnoresSweepError(t *testing.T) {
	s := &Sender{Wallet: &errSweepWallet{}, DustSat: 1000, SatPerKB: 100}
	n, err := s.Maintain(context.Background(), 0, 0)
	if err != nil {
		t.Fatalf("sweep error must not fail Maintain: %v", err)
	}
	if n != 0 {
		t.Fatalf("dustSwept=%d", n)
	}
}
