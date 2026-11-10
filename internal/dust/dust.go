// Package dust builds OP_FALSE OP_RETURN sweep scripts and chooses inputs
// so dust UTXOs can be spent even when each one is smaller than the fee.
package dust

import (
	"crypto/rand"
	"fmt"
	"sort"
	"time"

	"github.com/bsv-blockchain/go-sdk/script"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/sirdeggen/bsv-noise/internal/fee"
)

// OpReturnOutputBytes is an 8-byte value, a compact script length, and
// OP_FALSE OP_RETURN plus a 32-byte push (1+1+1+32).
const OpReturnOutputBytes = 44

// PayloadSize is the random data pushed after OP_RETURN.
const PayloadSize = 32

// DefaultMaxInputs caps one sweep so we batch-and-stop instead of emitting
// a chain of tiny sweeps.
const DefaultMaxInputs = 8

// DefaultCooldown is how long to wait between sweep transactions unless
// ForceCoins eligible dust is already sitting in the basket.
const DefaultCooldown = 3 * time.Minute

// ForceCoins triggers a sweep before the cooldown when at least this many
// eligible dust coins are waiting.
const ForceCoins = 8

// FeeMargin is added when deciding how many sats to put on the OP_RETURN
// so a slightly higher toolbox fee still does not mint a dust change output.
// It is kept under the toolbox dustFloor (~40 sats at 100 sat/kb).
const FeeMargin = 16

// Coin is one spendable output the wallet can unlock.
type Coin struct {
	Outpoint sdk.CreateActionInput // filled later; we keep Outpoint+sats here
	Satoshis uint64
	Input    sdk.CreateActionInput
}

// Plan is the input set for a sweep transaction.
type Plan struct {
	Inputs       []Coin
	Fee          int64
	FundingSats  uint64
	ExtraInput   bool // a non-dust input was added to cover the fee
	OpReturnSats uint64
}

// FalseReturnScript returns OP_FALSE OP_RETURN <32 random bytes>.
func FalseReturnScript() ([]byte, error) {
	payload := make([]byte, PayloadSize)
	if _, err := rand.Read(payload); err != nil {
		return nil, err
	}
	return FalseReturnScriptWith(payload)
}

// FalseReturnScriptWith builds the same script around a caller-supplied
// payload (tests pass a fixed 32-byte slice).
func FalseReturnScriptWith(payload []byte) ([]byte, error) {
	if len(payload) != PayloadSize {
		return nil, fmt.Errorf("OP_RETURN payload must be %d bytes", PayloadSize)
	}
	sc := &script.Script{}
	if err := sc.AppendOpcodes(script.OpFALSE, script.OpRETURN); err != nil {
		return nil, err
	}
	if err := sc.AppendPushData(payload); err != nil {
		return nil, err
	}
	return sc.Bytes(), nil
}

// EstimateFee is the toolbox-style fee for n P2PKH inputs and one OP_RETURN output.
func EstimateFee(inputs int, satPerKB int64) int64 {
	size := fee.OverheadBytes + inputs*fee.P2PKHInputBytes + OpReturnOutputBytes
	return fee.ForSize(size, satPerKB)
}

// Ready reports whether a new sweep should run. A zero last time means this
// process has not swept yet (and will, if any eligible dust exists). After a
// sweep, the cooldown always applies — even when many dust coins remain —
// so we batch-and-stop instead of sweeping on every payment. forceAt is
// retained for callers and tests but does not bypass a recent sweep.
func Ready(now, last time.Time, eligible int, cooldown time.Duration, forceAt int) bool {
	if eligible <= 0 {
		return false
	}
	if last.IsZero() {
		return true
	}
	if cooldown <= 0 {
		return true
	}
	return now.Sub(last) >= cooldown
}

// OutputSatoshis is the value to put on the sweep OP_RETURN. Leftover below
// threshold is burned there so the toolbox will not emit a dust change output.
// fee should already include FeeMargin so a slightly higher actual fee is still
// funded. Leftover at or above threshold stays 0 so the wallet can mint change.
func OutputSatoshis(funding uint64, fee int64, threshold uint64) uint64 {
	if fee < 0 || funding <= uint64(fee) {
		return 0
	}
	leftover := funding - uint64(fee)
	if leftover < threshold {
		return leftover
	}
	return 0
}

func finishPlan(chosen []Coin, total uint64, feeSat int64, extra bool, threshold uint64) Plan {
	return Plan{
		Inputs:       chosen,
		Fee:          feeSat,
		FundingSats:  total,
		ExtraInput:   extra,
		OpReturnSats: OutputSatoshis(total, feeSat+FeeMargin, threshold),
	}
}

// Select builds a sweep plan. Dust is every coin below threshold. One call
// batches as many dust inputs as fit in DefaultMaxInputs so a single
// transaction clears a pile and then stops; if they cannot cover the fee, the
// smallest non-dust coin is added (the wallet may then emit change).
func Select(coins []Coin, threshold uint64, satPerKB int64) (Plan, error) {
	return SelectN(coins, threshold, satPerKB, DefaultMaxInputs)
}

// SelectN is Select with an explicit input cap.
func SelectN(coins []Coin, threshold uint64, satPerKB int64, maxInputs int) (Plan, error) {
	if threshold < 1 {
		return Plan{}, fmt.Errorf("dust threshold must be positive")
	}
	if maxInputs < 1 {
		maxInputs = DefaultMaxInputs
	}
	var dust, rest []Coin
	for _, c := range coins {
		if c.Satoshis < threshold {
			dust = append(dust, c)
		} else {
			rest = append(rest, c)
		}
	}
	if len(dust) == 0 {
		return Plan{}, nil
	}
	sort.Slice(dust, func(i, j int) bool { return dust[i].Satoshis < dust[j].Satoshis })
	sort.Slice(rest, func(i, j int) bool { return rest[i].Satoshis < rest[j].Satoshis })

	var chosen []Coin
	var total uint64
	for _, c := range dust {
		if len(chosen) >= maxInputs {
			break
		}
		chosen = append(chosen, c)
		total += c.Satoshis
	}
	feeSat := EstimateFee(len(chosen), satPerKB)
	if feeSat > 0 && total >= uint64(feeSat) {
		return finishPlan(chosen, total, feeSat, false, threshold), nil
	}
	// The dust batch cannot pay the fee. Make room for a larger input if the cap is full.
	if len(chosen) >= maxInputs && len(chosen) > 0 {
		total -= chosen[0].Satoshis
		chosen = chosen[1:]
	}
	if len(rest) == 0 {
		return Plan{}, fmt.Errorf("dust totalling %d sats cannot cover the fee and no larger input is available", total)
	}
	extra := rest[0]
	chosen = append(chosen, extra)
	total += extra.Satoshis
	feeSat = EstimateFee(len(chosen), satPerKB)
	if feeSat <= 0 || total < uint64(feeSat) {
		return Plan{}, fmt.Errorf("selected inputs %d cannot cover fee %d", total, feeSat)
	}
	return finishPlan(chosen, total, feeSat, true, threshold), nil
}
