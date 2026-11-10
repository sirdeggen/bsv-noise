// Package benford splits a change amount the way the TypeScript BSV SDK
// does when Transaction.fee is called with changeDistribution "random".
//
// go-wallet-toolbox v0.191.4 has no such option: its funder splits change
// around MinimumDesiredUTXOValue. go-sdk v1.7.3 declares
// ChangeDistributionRandom but Fee() returns "not-implemented".
// The working implementation lives in the TS SDK:
// https://github.com/bsv-blockchain/ts-stack/blob/main/packages/sdk/src/transaction/Transaction.ts
// (distributeRandomChange / benfordNumber).
package benford

import (
	"fmt"
	"math"
	"math/rand"
)

// Number is the TS SDK benfordNumber(min, max): a value in [min, max]
// biased toward smaller leading digits via log10(1+1/d) for a random digit d.
func Number(min, max uint64, rnd *rand.Rand) uint64 {
	if max <= min {
		return min
	}
	d := rnd.Intn(9) + 1
	span := float64(max - min)
	return min + uint64(math.Floor(span*math.Log10(1+1/float64(d))/math.Log10(10)))
}

// Split partitions change satoshis into count outputs using the TS SDK
// random/Benford allocator. Each output is at least 1 sat; the last output
// receives the remainder so the parts sum to change. count must be >= 1
// and change must be >= count (one sat per output).
func Split(change, count uint64, rnd *rand.Rand) ([]uint64, error) {
	if count == 0 {
		return nil, fmt.Errorf("change output count must be positive")
	}
	if change < count {
		return nil, fmt.Errorf("change %d cannot fill %d outputs of at least 1 sat", change, count)
	}
	if count == 1 {
		return []uint64{change}, nil
	}
	out := make([]uint64, count)
	for i := range out {
		out[i] = 1
	}
	rest := change - count
	for i := uint64(0); i < count-1 && rest > 0; i++ {
		portion := Number(0, rest, rnd)
		out[i] += portion
		rest -= portion
	}
	out[count-1] += rest
	return out, nil
}

// SplitWithFloor is Split, then merges from the end until every part is at
// least floor (or a single remainder remains). Used when change outputs
// must stay above a dust or basket minimum.
func SplitWithFloor(change, count, floor uint64, rnd *rand.Rand) ([]uint64, error) {
	if floor <= 1 {
		return Split(change, count, rnd)
	}
	maxCount := change / floor
	if maxCount == 0 {
		return nil, fmt.Errorf("change %d is below floor %d", change, floor)
	}
	if count > maxCount {
		count = maxCount
	}
	if count < 1 {
		count = 1
	}
	// Give every output the floor first, then Benford-split the remainder
	// on top (the same 1-sat seed the TS allocator uses).
	parts, err := Split(change-count*(floor-1), count, rnd)
	if err != nil {
		return nil, err
	}
	for i := range parts {
		parts[i] += floor - 1
	}
	return parts, nil
}
