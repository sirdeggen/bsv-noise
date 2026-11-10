package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
)

func TestRejectsDisguiseFieldAndMainnetWithoutFlag(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.yaml")
	if err := os.WriteFile(path, []byte("disguise_as_commerce: true\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	env := map[string]string{"SERVER_PRIVATE_KEY": key.Hex()}
	getenv := func(k string) string { return env[k] }
	if _, err := Load(path, getenv); err == nil || !strings.Contains(err.Error(), "disguise_as_commerce") {
		t.Fatalf("disguise field err %v", err)
	}
	env["NETWORK"] = "main"
	env["BROADCAST"] = "true"
	if _, err := Load("", getenv); err == nil || !strings.Contains(err.Error(), "allow_mainnet_broadcast") {
		t.Fatalf("mainnet err %v", err)
	}
}

func TestWIFNetworkAndRedactedParseError(t *testing.T) {
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	wif := key.Wif()
	getenv := func(k string) string {
		if k == "SERVER_PRIVATE_KEY" {
			return wif
		}
		if k == "NETWORK" {
			return "test"
		}
		return ""
	}
	_, err = Load("", getenv)
	if err == nil || strings.Contains(err.Error(), wif) {
		t.Fatalf("wif leaked or accepted on testnet: %v", err)
	}
	getenv = func(k string) string {
		if k == "SERVER_PRIVATE_KEY" {
			return "not-a-key"
		}
		return ""
	}
	_, err = Load("", getenv)
	if err == nil || strings.Contains(err.Error(), "not-a-key") {
		t.Fatalf("invalid key leaked: %v", err)
	}
}

func TestDryRunLoads(t *testing.T) {
	key, err := ec.NewPrivateKey()
	if err != nil {
		t.Fatal(err)
	}
	rt, err := Load("", func(k string) string {
		if k == "SERVER_PRIVATE_KEY" {
			return key.Hex()
		}
		return ""
	})
	if err != nil {
		t.Fatal(err)
	}
	if rt.MessageBoxURL != "https://messagebox.bsvblockchain.tech" {
		t.Fatalf("message box %s", rt.MessageBoxURL)
	}
	if rt.Broadcast || rt.ImportFunding || rt.Mainnet {
		t.Fatalf("defaults %+v", rt.File)
	}
	if rt.Identity != key.PubKey().ToDERHex() {
		t.Fatal("identity mismatch")
	}
	if rt.StandardFee < rt.MinFeeSat || rt.StandardFee > rt.MaxFeeSat {
		t.Fatalf("fee %d", rt.StandardFee)
	}
}
