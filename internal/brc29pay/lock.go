// Package brc29pay builds BRC-29 locking scripts with the wallet toolbox's
// brc29 helpers. The derivation prefix and suffix are random base64 text.
// That text is the key id. Callers that internalize the output pass the raw
// bytes, which the wallet base64-encodes back to the same text.
package brc29pay

import (
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"fmt"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/brc29"
)

// Output is one controlled-recipient payment output.
type Output struct {
	LockingScript []byte
	Prefix        string
	Suffix        string
	PrefixRaw     []byte
	SuffixRaw     []byte
	Recipient     string
	Instructions  string
}

// Instructions is the remittance object carried in customInstructions.
type Instructions struct {
	DerivationPrefix string `json:"derivationPrefix"`
	DerivationSuffix string `json:"derivationSuffix"`
	Payee            string `json:"payee"`
	Purpose          string `json:"purpose"`
}

// Build locks satoshis to recipient using a fresh prefix and suffix.
// mainnet selects the address version used while hashing the pubkey. The
// P2PKH script itself depends on the pubkey hash, which is the same on both
// BSV networks; the flag is still passed so the toolbox address helper matches
// the configured network.
func Build(sender *ec.PrivateKey, recipient *ec.PublicKey, mainnet bool) (Output, error) {
	prefixRaw := make([]byte, 16)
	suffixRaw := make([]byte, 16)
	if _, err := rand.Read(prefixRaw); err != nil {
		return Output{}, err
	}
	if _, err := rand.Read(suffixRaw); err != nil {
		return Output{}, err
	}
	return BuildWith(sender, recipient, prefixRaw, suffixRaw, mainnet)
}

// BuildWith locks to an explicit prefix and suffix. The raw slices are
// base64-encoded to form the key id text.
func BuildWith(sender *ec.PrivateKey, recipient *ec.PublicKey, prefixRaw, suffixRaw []byte, mainnet bool) (Output, error) {
	if sender == nil || recipient == nil {
		return Output{}, fmt.Errorf("sender and recipient keys are required")
	}
	prefix := base64.StdEncoding.EncodeToString(prefixRaw)
	suffix := base64.StdEncoding.EncodeToString(suffixRaw)
	keyID := brc29.KeyID{DerivationPrefix: prefix, DerivationSuffix: suffix}
	locking, err := lock(sender, keyID, recipient, mainnet)
	if err != nil {
		return Output{}, err
	}
	inst, err := json.Marshal(Instructions{
		DerivationPrefix: prefix,
		DerivationSuffix: suffix,
		Payee:            recipient.ToDERHex(),
		Purpose:          "synthetic-load-test",
	})
	if err != nil {
		return Output{}, err
	}
	return Output{
		LockingScript: locking,
		Prefix:        prefix,
		Suffix:        suffix,
		PrefixRaw:     append([]byte(nil), prefixRaw...),
		SuffixRaw:     append([]byte(nil), suffixRaw...),
		Recipient:     recipient.ToDERHex(),
		Instructions:  string(inst),
	}, nil
}

func lock(sender *ec.PrivateKey, keyID brc29.KeyID, recipient *ec.PublicKey, mainnet bool) ([]byte, error) {
	var err error
	var sc interface{ Bytes() []byte }
	if mainnet {
		sc, err = brc29.LockForCounterparty(sender, keyID, recipient, brc29.WithMainNet())
	} else {
		sc, err = brc29.LockForCounterparty(sender, keyID, recipient, brc29.WithTestNet())
	}
	if err != nil {
		return nil, fmt.Errorf("brc29 lock: %w", err)
	}
	return sc.Bytes(), nil
}

// SameScript reports whether the recipient can derive the same locking script
// the sender built. This is the BRC-29 check internalizeAction performs.
func SameScript(senderPub *ec.PublicKey, recipientPriv *ec.PrivateKey, prefix, suffix string, mainnet bool) ([]byte, error) {
	keyID := brc29.KeyID{DerivationPrefix: prefix, DerivationSuffix: suffix}
	var sc interface{ Bytes() []byte }
	var err error
	if mainnet {
		sc, err = brc29.LockForSelf(senderPub, keyID, recipientPriv, brc29.WithMainNet())
	} else {
		sc, err = brc29.LockForSelf(senderPub, keyID, recipientPriv, brc29.WithTestNet())
	}
	if err != nil {
		return nil, err
	}
	return sc.Bytes(), nil
}
