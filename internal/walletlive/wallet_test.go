package walletlive

import (
	"context"
	"testing"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/defs"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wdk"
	"github.com/sirdeggen/bsv-noise/internal/brc29pay"
)

func TestEmptyWalletRejectsSpendAndBadImport(t *testing.T) {
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	w, err := Open(ctx, "test", false, key, key.PubKey().ToDERHex(), dir, "brc29-synthetic-load.local", "", "", 100, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	bal, err := w.Balance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 0 {
		t.Fatalf("balance %d", bal)
	}
	_, err = w.InternalizeAction(ctx, sdk.InternalizeActionArgs{
		Tx:          []byte{0x01, 0x02, 0x03},
		Description: "incoming BRC-29 message box payment",
		Outputs: []sdk.InternalizeOutput{{
			OutputIndex: 0,
			Protocol:    sdk.InternalizeProtocolWalletPayment,
		}},
	}, "brc29-synthetic-load.local")
	if err == nil {
		t.Fatal("invalid beef was accepted")
	}
	bal, err = w.Balance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 0 {
		t.Fatalf("balance after failed import %d", bal)
	}
	recipient, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	out, err := brc29pay.BuildWith(key, recipient.PubKey(), []byte("prefix-bytes"), []byte("suffix-bytes"), false)
	if err != nil {
		t.Fatal(err)
	}
	falseVal := false
	_, err = w.CreateAction(ctx, sdk.CreateActionArgs{
		Description: "synthetic BRC-29 load-test payment",
		Outputs: []sdk.CreateActionOutput{{
			LockingScript:      out.LockingScript,
			Satoshis:           100,
			OutputDescription:  "synthetic load-test payment to a controlled recipient",
			CustomInstructions: out.Instructions,
		}},
		Labels:  []string{"synthetic-load"},
		Options: &sdk.CreateActionOptions{RandomizeOutputs: &falseVal, AcceptDelayedBroadcast: &falseVal},
	}, "brc29-synthetic-load.local")
	if err == nil {
		t.Fatal("empty wallet broadcast a payment")
	}
	w.Close()
	w2, err := Open(ctx, "test", false, key, key.PubKey().ToDERHex(), dir, "brc29-synthetic-load.local", "", "", 100, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w2.Close()
	bal, err = w2.Balance(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if bal != 0 {
		t.Fatalf("restart balance %d", bal)
	}
}

func TestMainnetServicesConfigDisablesTAALAndSetsCallbackToken(t *testing.T) {
	identity := "0279be667ef9dcbbac55a06295ce870b07029bfcdb2dce28d959f2815b16f81798"
	cfg, err := servicesConfig(defs.NetworkMainnet, identity, "https://arc.taal.com", "token", nil)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.ArcConfig.Enabled {
		t.Fatal("TAAL ARC should be disabled")
	}
	if cfg.ArcGorillaPoolConfig.Enabled {
		t.Fatal("GorillaPool classic ARC should be disabled")
	}
	if !cfg.Arcade.Enabled {
		t.Fatal("primary Arcade should stay enabled")
	}
	if cfg.Arcade.URL != defs.ArcadeURL {
		t.Fatalf("primary Arcade URL %s", cfg.Arcade.URL)
	}
	if cfg.Arcade.EventsURL != defs.ArcadeURL {
		t.Fatalf("Arcade events URL %s", cfg.Arcade.EventsURL)
	}
	want := wdk.DeriveArcadeCallbackToken(identity)
	if cfg.Arcade.CallbackToken != want || want == "" {
		t.Fatalf("callback token %q want %q", cfg.Arcade.CallbackToken, want)
	}
	if !cfg.WhatsOnChain.Enabled {
		t.Fatal("WhatsOnChain should stay enabled")
	}
	if gorillaPoolArcadeURL != "https://arcade.gorillapool.io" {
		t.Fatalf("unexpected GorillaPool Arcade URL %s", gorillaPoolArcadeURL)
	}
}

func TestOpenStartsStorageMonitor(t *testing.T) {
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	dir := t.TempDir()
	w, err := Open(ctx, "test", false, key, key.PubKey().ToDERHex(), dir, "brc29-synthetic-load.local", "", "", 100, false, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if w.monitor == nil {
		t.Fatal("storage monitor was not started")
	}
	if w.svcCfg.ArcConfig.Enabled || w.svcCfg.ArcGorillaPoolConfig.Enabled {
		t.Fatal("ARC failovers should stay off on the test wallet")
	}
}
