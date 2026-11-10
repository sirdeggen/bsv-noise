package walletlive

import (
	"context"
	"testing"

	"github.com/bsv-blockchain/go-sdk/chainhash"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
)

func TestSweepBroadcastedRequiresTxidAndNoFailure(t *testing.T) {
	var zero chainhash.Hash
	var txid chainhash.Hash
	txid[0] = 1
	if SweepBroadcasted(zero, nil) {
		t.Fatal("zero txid is not broadcast")
	}
	if !SweepBroadcasted(txid, nil) {
		t.Fatal("non-zero txid with no send results should count")
	}
	if SweepBroadcasted(txid, []sdk.SendWithResult{{Status: sdk.ActionResultStatusFailed}}) {
		t.Fatal("failed send must not count")
	}
	if !SweepBroadcasted(txid, []sdk.SendWithResult{{Status: sdk.ActionResultStatusUnproven}}) {
		t.Fatal("unproven send is broadcast")
	}
}

type fakeSigner struct {
	ref    []byte
	result *sdk.SignActionResult
	err    error
	called bool
}

func (f *fakeSigner) SignAction(_ context.Context, args sdk.SignActionArgs, _ string) (*sdk.SignActionResult, error) {
	f.called = true
	f.ref = append([]byte(nil), args.Reference...)
	return f.result, f.err
}

func TestFinishSweepActionSignsWhenSignable(t *testing.T) {
	var txid chainhash.Hash
	txid[0] = 7
	signer := &fakeSigner{result: &sdk.SignActionResult{Txid: txid}}
	got, _, err := finishSweepAction(context.Background(), signer, &sdk.CreateActionResult{
		SignableTransaction: &sdk.SignableTransaction{Reference: []byte("sweep-ref")},
	}, "test.local")
	if err != nil {
		t.Fatal(err)
	}
	if !signer.called || string(signer.ref) != "sweep-ref" {
		t.Fatalf("SignAction not called with the create reference: %+v", signer)
	}
	if !got.IsEqual(&txid) {
		t.Fatalf("txid %s", got)
	}
}

func TestParseSQLiteTime(t *testing.T) {
	ts := parseSQLiteTime("2026-11-10 18:15:53.072164504+00:00")
	if ts.IsZero() || ts.UTC().Hour() != 18 {
		t.Fatalf("parsed %v", ts)
	}
}
