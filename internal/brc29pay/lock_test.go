package brc29pay

import (
	"bytes"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
)

func TestSenderAndRecipientDeriveTheSameScript(t *testing.T) {
	sender, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	recipient, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	for _, mainnet := range []bool{true, false} {
		out, err := BuildWith(sender, recipient.PubKey(), []byte("prefix-bytes"), []byte("suffix-bytes"), mainnet)
		if err != nil {
			t.Fatal(err)
		}
		got, err := SameScript(sender.PubKey(), recipient, out.Prefix, out.Suffix, mainnet)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(out.LockingScript, got) {
			t.Fatalf("mainnet=%v scripts differ\nsender %x\nrecip  %x", mainnet, out.LockingScript, got)
		}
		// Paying the same identity (funding key == spending key) still matches,
		// because both sides use counterparty type "other" with that identity.
		self, err := BuildWith(sender, sender.PubKey(), []byte("self-prefix"), []byte("self-suffix"), mainnet)
		if err != nil {
			t.Fatal(err)
		}
		back, err := SameScript(sender.PubKey(), sender, self.Prefix, self.Suffix, mainnet)
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(self.LockingScript, back) {
			t.Fatalf("self payment scripts differ on mainnet=%v", mainnet)
		}
	}
}
