// Package fee estimates a P2PKH transaction fee the same way the wallet
// toolbox sizes fuel: ceil(size/1000 * satoshis_per_kb).
//
// The toolbox implementation is feeForSize in
// github.com/bsv-blockchain/go-wallet-toolbox/pkg/defs/utxo_management.go.
// This copy exists so the generator can refuse a payment before CreateAction
// when the estimate is outside the operator's per-transaction bounds.
package fee

import "math"

const (
	// OverheadBytes is the version, locktime and input/output count prefix
	// of a small legacy transaction.
	OverheadBytes = 10
	// P2PKHInputBytes matches the toolbox fuel input size (compressed pubkey,
	// DER signature, script wrapper).
	P2PKHInputBytes = 148
	// P2PKHOutputBytes is a standard P2PKH output (8 byte value + 25 byte script).
	P2PKHOutputBytes = 34
)

// TxSize estimates the serialized size of a P2PKH transaction.
func TxSize(inputs, outputs int) int {
	if inputs < 0 {
		inputs = 0
	}
	if outputs < 0 {
		outputs = 0
	}
	return OverheadBytes + inputs*P2PKHInputBytes + outputs*P2PKHOutputBytes
}

// ForSize returns the integer satoshi fee for a transaction of the given size
// at satPerKB. The formula matches the toolbox float ceil.
func ForSize(size int, satPerKB int64) int64 {
	if size <= 0 || satPerKB <= 0 {
		return 0
	}
	return int64(math.Ceil(float64(size) / 1000.0 * float64(satPerKB)))
}

// Standard is the fee for one P2PKH input and two outputs (payment + change),
// which is the shape CreateAction produces for a single funded payment.
func Standard(satPerKB int64) int64 {
	return ForSize(TxSize(1, 2), satPerKB)
}
