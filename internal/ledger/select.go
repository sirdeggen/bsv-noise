// Package ledger chooses, splits and merges integer-satoshi UTXOs.
// It is the dry-run coin selector. The live wallet uses go-wallet-toolbox
// coin selection instead; this package never talks to a network.
package ledger

import (
	"errors"
	"sort"
)

// UTXO is one spendable output. Outpoint is an opaque id, not necessarily a
// chain outpoint. Dry-run ids are prefixed so they cannot be mistaken for
// mined transactions.
type UTXO struct {
	Outpoint   string
	Satoshis   int64
	ReservedBy string
}

var (
	ErrInsufficient = errors.New("insufficient funds")
	ErrNoSplit      = errors.New("nothing to split")
	ErrNoMerge      = errors.New("nothing to merge")
)

// Select picks free UTXOs that cover need satoshis.
// It prefers the smallest single UTXO that covers need, otherwise the
// smallest free outputs, and never returns more than maxInputs.
func Select(utxos []UTXO, need int64, maxInputs int) ([]UTXO, error) {
	if need <= 0 {
		return nil, errors.New("need must be positive")
	}
	if maxInputs < 1 {
		return nil, errors.New("max inputs must be positive")
	}
	free := make([]UTXO, 0, len(utxos))
	for _, u := range utxos {
		if u.ReservedBy == "" && u.Satoshis > 0 {
			free = append(free, u)
		}
	}
	sort.Slice(free, func(i, j int) bool {
		if free[i].Satoshis == free[j].Satoshis {
			return free[i].Outpoint < free[j].Outpoint
		}
		return free[i].Satoshis < free[j].Satoshis
	})
	for _, u := range free {
		if u.Satoshis >= need {
			return []UTXO{u}, nil
		}
	}
	if len(free) == 0 {
		return nil, ErrInsufficient
	}
	var picked []UTXO
	var sum int64
	for _, u := range free {
		if len(picked) >= maxInputs {
			break
		}
		picked = append(picked, u)
		sum += u.Satoshis
		if sum >= need {
			return picked, nil
		}
	}
	return nil, ErrInsufficient
}

// SplitPlan breaks one free output into n new outputs, subtracting fee once.
// Parts are as equal as integer division allows. The remainder stays on the
// first part so no satoshi is dropped.
func SplitPlan(src UTXO, n int, fee int64) ([]int64, error) {
	if src.ReservedBy != "" {
		return nil, ErrNoSplit
	}
	if n < 2 {
		return nil, errors.New("split needs at least 2 outputs")
	}
	if fee < 0 || src.Satoshis <= fee {
		return nil, ErrNoSplit
	}
	body := src.Satoshis - fee
	each := body / int64(n)
	if each < 1 {
		return nil, ErrNoSplit
	}
	parts := make([]int64, n)
	var used int64
	for i := range parts {
		parts[i] = each
		used += each
	}
	parts[0] += body - used
	return parts, nil
}

// MergePlan spends the smallest free outputs until count would fall to target,
// keeping one new output of (sum - fee).
func MergePlan(utxos []UTXO, target int, fee int64) (spent []UTXO, created int64, err error) {
	free := make([]UTXO, 0, len(utxos))
	for _, u := range utxos {
		if u.ReservedBy == "" && u.Satoshis > 0 {
			free = append(free, u)
		}
	}
	if target < 1 || len(free) <= target {
		return nil, 0, ErrNoMerge
	}
	sort.Slice(free, func(i, j int) bool {
		if free[i].Satoshis == free[j].Satoshis {
			return free[i].Outpoint < free[j].Outpoint
		}
		return free[i].Satoshis < free[j].Satoshis
	})
	// Leave `target` outputs in place, plus one new output replaces the spent set,
	// so we spend len(free)-target+? Wait: spending k and creating 1 changes
	// count by 1-k. We want len-k+1 <= target => k >= len-target+1.
	need := len(free) - target + 1
	if need < 2 {
		need = 2
	}
	if need > len(free) {
		need = len(free)
	}
	spent = free[:need]
	var sum int64
	for _, u := range spent {
		sum += u.Satoshis
	}
	if sum <= fee {
		return nil, 0, ErrNoMerge
	}
	return spent, sum - fee, nil
}
