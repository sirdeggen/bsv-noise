package dust

import (
	"testing"
	"time"

	"github.com/bsv-blockchain/go-sdk/script"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
)

func TestFalseReturnScriptShape(t *testing.T) {
	payload := make([]byte, 32)
	for i := range payload {
		payload[i] = byte(i)
	}
	b, err := FalseReturnScriptWith(payload)
	if err != nil {
		t.Fatal(err)
	}
	if len(b) < 35 || b[0] != script.OpFALSE || b[1] != script.OpRETURN {
		t.Fatalf("script %x", b)
	}
	if !script.NewFromBytes(b).IsData() {
		t.Fatalf("expected OP_FALSE OP_RETURN data script, got %x", b)
	}
}

func TestSelectBatchesDustUntilFeeCovered(t *testing.T) {
	coins := []Coin{
		{Satoshis: 20, Input: sdk.CreateActionInput{InputDescription: "a"}},
		{Satoshis: 20, Input: sdk.CreateActionInput{InputDescription: "b"}},
		{Satoshis: 20, Input: sdk.CreateActionInput{InputDescription: "c"}},
		{Satoshis: 50_000, Input: sdk.CreateActionInput{InputDescription: "big"}},
	}
	plan, err := Select(coins, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	// One 10-sat coin cannot cover a ~21 sat 1-in fee; several dust must be batched.
	if plan.ExtraInput {
		t.Fatalf("did not need a fee input: %+v", plan)
	}
	if len(plan.Inputs) < 2 || plan.FundingSats < uint64(plan.Fee) {
		t.Fatalf("plan %+v", plan)
	}
	for _, in := range plan.Inputs {
		if in.Satoshis >= 1000 {
			t.Fatalf("non-dust included %d", in.Satoshis)
		}
	}
}

func TestSelectAddsFeeInputWhenDustIsTooSmall(t *testing.T) {
	coins := []Coin{
		{Satoshis: 10, Input: sdk.CreateActionInput{InputDescription: "dust"}},
		{Satoshis: 80_000, Input: sdk.CreateActionInput{InputDescription: "fat"}},
		{Satoshis: 5_000, Input: sdk.CreateActionInput{InputDescription: "mid"}},
	}
	plan, err := Select(coins, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if !plan.ExtraInput {
		t.Fatal("expected a non-dust input")
	}
	if plan.Inputs[len(plan.Inputs)-1].Satoshis != 5_000 {
		t.Fatalf("should pick the smallest non-dust, got %+v", plan.Inputs)
	}
}

func TestSelectNone(t *testing.T) {
	plan, err := Select([]Coin{{Satoshis: 5000}}, 1000, 100)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Inputs) != 0 {
		t.Fatalf("unexpected %+v", plan)
	}
}

func TestSelectTakesABatchUpToMaxInputs(t *testing.T) {
	var coins []Coin
	for i := 0; i < 12; i++ {
		coins = append(coins, Coin{Satoshis: 80})
	}
	plan, err := SelectN(coins, 1000, 100, 8)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Inputs) != 8 || plan.ExtraInput {
		t.Fatalf("expected 8 dust inputs, got %+v", plan)
	}
	if plan.OpReturnSats == 0 {
		t.Fatal("leftover below threshold should be burned on the OP_RETURN")
	}
	if plan.FundingSats-uint64(plan.Fee+FeeMargin) != plan.OpReturnSats {
		t.Fatalf("burn %d does not match leftover of %+v", plan.OpReturnSats, plan)
	}
}

func TestOutputSatoshisBurnsOnlySubThresholdLeftover(t *testing.T) {
	if got := OutputSatoshis(80, 24+FeeMargin, 1000); got != 80-(24+FeeMargin) {
		t.Fatalf("burn got %d", got)
	}
	if got := OutputSatoshis(50_000, 24+FeeMargin, 1000); got != 0 {
		t.Fatalf("large leftover should stay 0 for toolbox change, got %d", got)
	}
	if got := OutputSatoshis(20, 40, 1000); got != 0 {
		t.Fatalf("underfunded should be 0, got %d", got)
	}
}

func TestReadyCooldownAndForce(t *testing.T) {
	now := time.Date(2026, 11, 10, 12, 0, 0, 0, time.UTC)
	if Ready(now, time.Time{}, 0, DefaultCooldown, ForceCoins) {
		t.Fatal("no coins should not be ready")
	}
	if !Ready(now, time.Time{}, 1, DefaultCooldown, ForceCoins) {
		t.Fatal("first sweep with any dust should run")
	}
	last := now.Add(-time.Minute)
	if Ready(now, last, 3, DefaultCooldown, ForceCoins) {
		t.Fatal("should wait out the cooldown")
	}
	if Ready(now, last, ForceCoins, DefaultCooldown, ForceCoins) {
		t.Fatal("a large dust pile must not bypass a recent sweep")
	}
	if !Ready(now, now.Add(-DefaultCooldown), 1, DefaultCooldown, ForceCoins) {
		t.Fatal("after cooldown a single dust coin should sweep")
	}
}
