// Package peerpay parses the payment_inbox body used by PeerPay.
//
// A Message Box is not a P2PKH address. A funder sends a BRC-29 payment whose
// PeerPay token is JSON in the recipient's payment_inbox. The token carries
// an Atomic BEEF byte array (JSON integers 0–255), base64 derivation prefix
// and suffix, an amount, and an optional output index. A base64 string is not
// accepted as the transaction field; BRC-29 and the deployed PeerPay client
// keep that encoding for other transports such as BRC-105.
package peerpay

import (
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/transaction"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
)

const ProtocolWalletPayment = "wallet payment"

// Token is the JSON object PeerPay stores as the message body.
type Token struct {
	CustomInstructions struct {
		DerivationPrefix string `json:"derivationPrefix"`
		DerivationSuffix string `json:"derivationSuffix"`
	} `json:"customInstructions"`
	Transaction json.RawMessage `json:"transaction"`
	Amount      int64           `json:"amount"`
	OutputIndex *uint32         `json:"outputIndex"`
}

// Incoming is one payment_inbox message with its transaction bytes decoded.
type Incoming struct {
	MessageID   string
	Sender      string
	SenderKey   *ec.PublicKey
	Amount      int64
	OutputIndex uint32
	Tx          []byte
	TxID        string
	Satoshis    uint64
	Prefix      string
	Suffix      string
	PrefixRaw   []byte
	SuffixRaw   []byte
}

var (
	ErrNotPayment = errors.New("message body is not a PeerPay payment token")
	ErrTxEncoding = errors.New("transaction must be a JSON array of bytes 0-255")
)

// ParseBody interprets a listMessages body. The body may be a JSON object or
// a JSON string containing that object (the shape produced by JSON.stringify).
func ParseBody(messageID, sender string, body []byte) (Incoming, error) {
	body = unwrapString(body)
	var tok Token
	if err := json.Unmarshal(body, &tok); err != nil {
		return Incoming{}, ErrNotPayment
	}
	if tok.CustomInstructions.DerivationPrefix == "" || tok.CustomInstructions.DerivationSuffix == "" {
		return Incoming{}, ErrNotPayment
	}
	raw, err := DecodeTxBytes(tok.Transaction)
	if err != nil {
		return Incoming{}, err
	}
	prefixRaw, err := base64.StdEncoding.DecodeString(tok.CustomInstructions.DerivationPrefix)
	if err != nil || len(prefixRaw) == 0 {
		return Incoming{}, fmt.Errorf("derivation prefix is not base64")
	}
	suffixRaw, err := base64.StdEncoding.DecodeString(tok.CustomInstructions.DerivationSuffix)
	if err != nil || len(suffixRaw) == 0 {
		return Incoming{}, fmt.Errorf("derivation suffix is not base64")
	}
	// The wallet re-encodes these bytes with standard base64 and uses that
	// text as the BRC-29 key id. Reject prefixes that would not round-trip.
	if base64.StdEncoding.EncodeToString(prefixRaw) != tok.CustomInstructions.DerivationPrefix ||
		base64.StdEncoding.EncodeToString(suffixRaw) != tok.CustomInstructions.DerivationSuffix {
		return Incoming{}, fmt.Errorf("derivation text is not canonical base64")
	}
	senderKey, err := ec.PublicKeyFromString(sender)
	if err != nil {
		return Incoming{}, fmt.Errorf("sender is not an identity key")
	}
	var index uint32
	if tok.OutputIndex != nil {
		index = *tok.OutputIndex
	}
	in := Incoming{
		MessageID:   messageID,
		Sender:      senderKey.ToDERHex(),
		SenderKey:   senderKey,
		Amount:      tok.Amount,
		OutputIndex: index,
		Tx:          raw,
		Prefix:      tok.CustomInstructions.DerivationPrefix,
		Suffix:      tok.CustomInstructions.DerivationSuffix,
		PrefixRaw:   prefixRaw,
		SuffixRaw:   suffixRaw,
	}
	txid, sats, err := outputSatoshis(raw, index)
	if err != nil {
		return Incoming{}, err
	}
	in.TxID = txid
	in.Satoshis = sats
	if tok.Amount > 0 && uint64(tok.Amount) != sats {
		return Incoming{}, fmt.Errorf("token amount %d does not match output %d", tok.Amount, sats)
	}
	return in, nil
}

func unwrapString(body []byte) []byte {
	var s string
	if err := json.Unmarshal(body, &s); err == nil && len(s) > 0 && (s[0] == '{' || s[0] == '[') {
		return []byte(s)
	}
	return body
}

// DecodeTxBytes accepts a JSON numeric array or a contiguous numeric-key
// object, which is the compatibility form described by BRC-29. A JSON string
// is rejected.
func DecodeTxBytes(raw json.RawMessage) ([]byte, error) {
	if len(raw) == 0 {
		return nil, ErrTxEncoding
	}
	var v any
	if err := json.Unmarshal(raw, &v); err != nil {
		return nil, ErrTxEncoding
	}
	switch t := v.(type) {
	case []any:
		return nums(t)
	case map[string]any:
		return numericObject(t)
	default:
		return nil, ErrTxEncoding
	}
}

func nums(in []any) ([]byte, error) {
	if len(in) == 0 {
		return nil, ErrTxEncoding
	}
	out := make([]byte, len(in))
	for i, n := range in {
		b, err := asByte(n)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

func numericObject(m map[string]any) ([]byte, error) {
	if len(m) == 0 {
		return nil, ErrTxEncoding
	}
	out := make([]byte, len(m))
	for k, n := range m {
		i, err := strconv.Atoi(k)
		if err != nil || i < 0 || i >= len(m) {
			return nil, ErrTxEncoding
		}
		b, err := asByte(n)
		if err != nil {
			return nil, err
		}
		out[i] = b
	}
	return out, nil
}

func asByte(n any) (byte, error) {
	f, ok := n.(float64)
	if !ok || f < 0 || f > 255 || f != float64(int(f)) {
		return 0, ErrTxEncoding
	}
	return byte(f), nil
}

func outputSatoshis(raw []byte, index uint32) (string, uint64, error) {
	beef, txHash, err := transaction.NewBeefFromAtomicBytes(raw)
	if err != nil {
		return "", 0, fmt.Errorf("transaction is not atomic beef: %w", err)
	}
	tx := beef.FindAtomicTransactionByHash(txHash)
	if tx == nil || txHash == nil {
		return "", 0, fmt.Errorf("atomic beef has no subject transaction")
	}
	if int(index) >= len(tx.Outputs) {
		return "", 0, fmt.Errorf("output index %d is outside the transaction", index)
	}
	return txHash.String(), tx.Outputs[index].Satoshis, nil
}

// InternalizeArgs is the BRC-100 request the recipient wallet expects.
// Prefix and suffix are the raw bytes whose standard base64 form is the key id.
func (in Incoming) InternalizeArgs(description string) sdk.InternalizeActionArgs {
	if description == "" {
		description = "incoming BRC-29 message box payment"
	}
	return sdk.InternalizeActionArgs{
		Tx:          in.Tx,
		Description: description,
		Labels:      []string{"message-box", "synthetic-load-funding"},
		Outputs: []sdk.InternalizeOutput{{
			OutputIndex: in.OutputIndex,
			Protocol:    sdk.InternalizeProtocolWalletPayment,
			PaymentRemittance: &sdk.Payment{
				DerivationPrefix:  in.PrefixRaw,
				DerivationSuffix:  in.SuffixRaw,
				SenderIdentityKey: in.SenderKey,
			},
		}},
	}
}
