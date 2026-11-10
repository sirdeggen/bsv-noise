package peerpay

import (
	"encoding/base64"
	"encoding/json"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	"github.com/bsv-blockchain/go-sdk/script"
	"github.com/bsv-blockchain/go-sdk/transaction"
	"github.com/bsv-blockchain/go-sdk/transaction/template/p2pkh"
)

func TestDecodeRejectsBase64String(t *testing.T) {
	raw := json.RawMessage(`"AAF/gP8="`)
	if _, err := DecodeTxBytes(raw); err != ErrTxEncoding {
		t.Fatalf("err %v", err)
	}
	good := json.RawMessage(`[0,1,127,128,255]`)
	got, err := DecodeTxBytes(good)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != string([]byte{0, 1, 127, 128, 255}) {
		t.Fatalf("bytes %v", got)
	}
	obj := json.RawMessage(`{"0":0,"1":1,"2":127,"3":128,"4":255}`)
	got, err = DecodeTxBytes(obj)
	if err != nil {
		t.Fatal(err)
	}
	if len(got) != 5 || got[2] != 127 || got[4] != 255 {
		t.Fatalf("object bytes %v", got)
	}
	if _, err := DecodeTxBytes(json.RawMessage(`[0,-1,2]`)); err != ErrTxEncoding {
		t.Fatalf("negative %v", err)
	}
}

func TestParseAtomicBeefRoundTrip(t *testing.T) {
	sender, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	addr, err := script.NewAddressFromPublicKey(recipient.PubKey(), true)
	if err != nil {
		t.Fatal(err)
	}
	lock, err := p2pkh.Lock(addr)
	if err != nil {
		t.Fatal(err)
	}
	tx := transaction.NewTransaction()
	tx.AddOutput(&transaction.TransactionOutput{Satoshis: 42, LockingScript: lock})
	beef := transaction.NewBeef()
	if _, err := beef.MergeTransaction(tx); err != nil {
		t.Fatal(err)
	}
	raw, err := beef.AtomicBytes(tx.TxID())
	if err != nil {
		t.Fatal(err)
	}
	nums := make([]int, len(raw))
	for i, b := range raw {
		nums[i] = int(b)
	}
	prefix := base64.StdEncoding.EncodeToString([]byte("pay-prefix"))
	suffix := base64.StdEncoding.EncodeToString([]byte("out-suffix"))
	body, err := json.Marshal(map[string]any{
		"customInstructions": map[string]string{
			"derivationPrefix": prefix,
			"derivationSuffix": suffix,
		},
		"transaction": nums,
		"amount":      42,
	})
	if err != nil {
		t.Fatal(err)
	}
	// listMessages returns the body as a JSON string.
	quoted, err := json.Marshal(string(body))
	if err != nil {
		t.Fatal(err)
	}
	in, err := ParseBody("m-1", sender.PubKey().ToDERHex(), quoted)
	if err != nil {
		t.Fatal(err)
	}
	if in.Satoshis != 42 || in.OutputIndex != 0 || in.Prefix != prefix {
		t.Fatalf("parsed %+v", in)
	}
	args := in.InternalizeArgs("incoming BRC-29 message box payment")
	if args.Outputs[0].Protocol != ProtocolWalletPayment {
		t.Fatalf("protocol %s", args.Outputs[0].Protocol)
	}
	if base64.StdEncoding.EncodeToString(args.Outputs[0].PaymentRemittance.DerivationPrefix) != prefix {
		t.Fatal("prefix did not round-trip to the key-id text")
	}
	if args.Outputs[0].PaymentRemittance.SenderIdentityKey.ToDERHex() != sender.PubKey().ToDERHex() {
		t.Fatal("sender identity mismatch")
	}
}
