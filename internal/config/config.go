// Package config loads the operator file and environment.
//
// SERVER_PRIVATE_KEY is never taken from the YAML file. It comes from the
// environment or a secret file, and it is not retained in the struct that
// YAML unmarshalling fills.
package config

import (
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/sirdeggen/bsv-noise/internal/fee"
	"github.com/sirdeggen/bsv-noise/internal/schedule"
	"gopkg.in/yaml.v3"

	base58 "github.com/bsv-blockchain/go-sdk/compat/base58"
	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
)

const (
	wifMainNet = 0x80
	wifTestNet = 0xef
)

// File is the YAML document. Unknown fields are rejected so a flag whose
// purpose is disguising traffic cannot be smuggled in.
type File struct {
	Network               string  `yaml:"network"`
	Listen                string  `yaml:"listen"`
	DataDir               string  `yaml:"data_dir"`
	Originator            string  `yaml:"originator"`
	Broadcast             bool    `yaml:"broadcast"`
	AllowMainnetBroadcast bool    `yaml:"allow_mainnet_broadcast"`
	ImportFunding         bool    `yaml:"import_funding"`
	ControlledRecipients  bool    `yaml:"controlled_recipients"`
	MessageBoxURL         string  `yaml:"message_box_url"`
	MessageBox            string  `yaml:"message_box"`
	SweepInterval         string  `yaml:"sweep_interval"`
	TargetTPS             float64 `yaml:"target_tps"`
	MinAmount             int64   `yaml:"min_amount"`
	MaxAmount             int64   `yaml:"max_amount"`
	AmountDistribution    string  `yaml:"amount_distribution"`
	Seed                  int64   `yaml:"seed"`
	MinFeeSat             int64   `yaml:"min_fee_sat"`
	MaxFeeSat             int64   `yaml:"max_fee_sat"`
	SatPerKB              int64   `yaml:"sat_per_kb"`
	DailySpendCap         int64   `yaml:"daily_spend_cap"`
	DailyFeeCap           int64   `yaml:"daily_fee_cap"`
	ReserveSat            int64   `yaml:"reserve_sat"`
	MaxInFlight           int     `yaml:"max_in_flight"`
	ConfirmAfter          string  `yaml:"confirm_after"`
	MaxUTXOs              int     `yaml:"max_utxos"`
	MinUTXOs              int     `yaml:"min_utxos"`
	MaxInputs             int     `yaml:"max_inputs"`
	SimBalance            int64   `yaml:"sim_balance"`
	GenerateRecipients    int     `yaml:"generate_recipients"`
	RecipientIdentityKeys string  `yaml:"recipient_identity_keys"`
	UTXOStrategy          string  `yaml:"utxo_strategy"`
	MetricsToken          string  `yaml:"metrics_token"`
	ConsoleToken          string  `yaml:"console_token"`
	MaxPayments           int     `yaml:"max_payments"`
	ShutdownTimeout       string  `yaml:"shutdown_timeout"`
	LogLevel              string  `yaml:"log_level"`
	ARCURL                string  `yaml:"arc_url"`
	DustSat               int64   `yaml:"dust_sat"`
	TargetChangeOutputs   int     `yaml:"target_change_outputs"`
	MinDesiredUTXOValue   int64   `yaml:"min_desired_utxo_value"`
}

// Runtime is the validated process configuration.
type Runtime struct {
	File
	SweepEvery      time.Duration
	ConfirmEvery    time.Duration
	ShutdownEvery   time.Duration
	Distribution    schedule.AmountDistribution
	Mainnet         bool
	Private         *ec.PrivateKey
	Identity        string
	Recipients      []string
	MetricsBearer   string
	ConsoleBearer   string
	ARCToken        string
	StandardFee     int64
	ExternalEffects bool
}

// Defaults returns the dry-run configuration.
func Defaults() File {
	return File{
		Network:              "test",
		Listen:               "127.0.0.1:8741",
		DataDir:              "data",
		Originator:           "brc29-synthetic-load.local",
		Broadcast:            false,
		ImportFunding:        false,
		ControlledRecipients: true,
		MessageBoxURL:        "https://messagebox.bsvblockchain.tech",
		MessageBox:           "payment_inbox",
		SweepInterval:        "15s",
		TargetTPS:            0.5,
		MinAmount:            100,
		MaxAmount:            2000,
		AmountDistribution:   "realistic",
		MinFeeSat:            1,
		MaxFeeSat:            2000,
		SatPerKB:             100,
		DailySpendCap:        100_000_000,
		DailyFeeCap:          1_000_000,
		ReserveSat:           50_000,
		MaxInFlight:          4,
		ConfirmAfter:         "2s",
		MaxUTXOs:             128,
		MinUTXOs:             1,
		MaxInputs:            8,
		SimBalance:           10_000_000,
		GenerateRecipients:   1,
		UTXOStrategy:         "privacy",
		ShutdownTimeout:      "10s",
		LogLevel:             "info",
		DustSat:              1000,
		TargetChangeOutputs:  8,
		MinDesiredUTXOValue:  1000,
	}
}

// Load reads YAML if path is non-empty, then applies env. getenv may be nil.
func Load(path string, getenv func(string) string) (Runtime, error) {
	if getenv == nil {
		getenv = os.Getenv
	}
	cfg := Defaults()
	if path == "" {
		path = getenv("CONFIG_FILE")
	}
	if path != "" {
		buf, err := os.ReadFile(path)
		if err != nil {
			return Runtime{}, fmt.Errorf("read config: %w", err)
		}
		dec := yaml.NewDecoder(strings.NewReader(string(buf)))
		dec.KnownFields(true)
		if err := dec.Decode(&cfg); err != nil && !errors.Is(err, io.EOF) {
			return Runtime{}, fmt.Errorf("parse config: %w", err)
		}
	}
	applyEnv(&cfg, getenv)
	rt, err := cfg.Validate()
	if err != nil {
		return Runtime{}, err
	}
	key, identity, err := loadKey(getenv, rt.Mainnet)
	if err != nil {
		return Runtime{}, err
	}
	rt.Private = key
	rt.Identity = identity
	if tok := strings.TrimSpace(getenv("METRICS_TOKEN")); tok != "" {
		rt.MetricsBearer = tok
	} else if rt.MetricsToken != "" {
		rt.MetricsBearer = rt.MetricsToken
		rt.MetricsToken = ""
	}
	if file := getenv("METRICS_TOKEN_FILE"); file != "" && rt.MetricsBearer == "" {
		buf, err := os.ReadFile(file)
		if err != nil {
			return Runtime{}, fmt.Errorf("read metrics token file: %w", err)
		}
		rt.MetricsBearer = strings.TrimSpace(string(buf))
	}
	if tok := strings.TrimSpace(getenv("CONSOLE_TOKEN")); tok != "" {
		rt.ConsoleBearer = tok
	} else if rt.ConsoleToken != "" {
		rt.ConsoleBearer = rt.ConsoleToken
		rt.ConsoleToken = ""
	}
	if file := getenv("CONSOLE_TOKEN_FILE"); file != "" && rt.ConsoleBearer == "" {
		buf, err := os.ReadFile(file)
		if err != nil {
			return Runtime{}, fmt.Errorf("read console token file: %w", err)
		}
		rt.ConsoleBearer = strings.TrimSpace(string(buf))
	}
	if tok := getenv("ARC_TOKEN"); tok != "" {
		rt.ARCToken = strings.TrimSpace(tok)
	} else if file := getenv("ARC_TOKEN_FILE"); file != "" {
		buf, err := os.ReadFile(file)
		if err != nil {
			return Runtime{}, fmt.Errorf("read ARC token file: %w", err)
		}
		rt.ARCToken = strings.TrimSpace(string(buf))
	}
	rt.ExternalEffects = rt.Broadcast || rt.ImportFunding
	return rt, nil
}

func applyEnv(cfg *File, getenv func(string) string) {
	set := func(key string, dest *string) {
		if v := getenv(key); v != "" {
			*dest = v
		}
	}
	set("NETWORK", &cfg.Network)
	set("LISTEN", &cfg.Listen)
	set("DATA_DIR", &cfg.DataDir)
	set("ORIGINATOR", &cfg.Originator)
	set("MESSAGE_BOX_URL", &cfg.MessageBoxURL)
	set("MESSAGE_BOX", &cfg.MessageBox)
	set("SWEEP_INTERVAL", &cfg.SweepInterval)
	set("AMOUNT_DISTRIBUTION", &cfg.AmountDistribution)
	set("CONFIRM_AFTER", &cfg.ConfirmAfter)
	set("RECIPIENT_IDENTITY_KEYS", &cfg.RecipientIdentityKeys)
	set("UTXO_STRATEGY", &cfg.UTXOStrategy)
	set("SHUTDOWN_TIMEOUT", &cfg.ShutdownTimeout)
	set("LOG_LEVEL", &cfg.LogLevel)
	set("ARC_URL", &cfg.ARCURL)
	setBool := func(key string, dest *bool) {
		if v := getenv(key); v != "" {
			b, err := strconv.ParseBool(v)
			if err == nil {
				*dest = b
			}
		}
	}
	setBool("BROADCAST", &cfg.Broadcast)
	setBool("ALLOW_MAINNET_BROADCAST", &cfg.AllowMainnetBroadcast)
	setBool("IMPORT_FUNDING", &cfg.ImportFunding)
	setBool("CONTROLLED_RECIPIENTS", &cfg.ControlledRecipients)
	setInt := func(key string, dest *int64) {
		if v := getenv(key); v != "" {
			n, err := strconv.ParseInt(v, 10, 64)
			if err == nil {
				*dest = n
			}
		}
	}
	setInt("MIN_AMOUNT", &cfg.MinAmount)
	setInt("MAX_AMOUNT", &cfg.MaxAmount)
	setInt("SEED", &cfg.Seed)
	setInt("MIN_FEE_SAT", &cfg.MinFeeSat)
	setInt("MAX_FEE_SAT", &cfg.MaxFeeSat)
	setInt("SAT_PER_KB", &cfg.SatPerKB)
	setInt("DAILY_SPEND_CAP", &cfg.DailySpendCap)
	setInt("DAILY_FEE_CAP", &cfg.DailyFeeCap)
	setInt("RESERVE_SAT", &cfg.ReserveSat)
	setInt("SIM_BALANCE", &cfg.SimBalance)
	setInt("DUST_SAT", &cfg.DustSat)
	setInt("MIN_DESIRED_UTXO_VALUE", &cfg.MinDesiredUTXOValue)
	if v := getenv("TARGET_TPS"); v != "" {
		if n, err := strconv.ParseFloat(v, 64); err == nil {
			cfg.TargetTPS = n
		}
	}
	setN := func(key string, dest *int) {
		if v := getenv(key); v != "" {
			n, err := strconv.Atoi(v)
			if err == nil {
				*dest = n
			}
		}
	}
	setN("MAX_IN_FLIGHT", &cfg.MaxInFlight)
	setN("MAX_UTXOS", &cfg.MaxUTXOs)
	setN("MIN_UTXOS", &cfg.MinUTXOs)
	setN("MAX_INPUTS", &cfg.MaxInputs)
	setN("GENERATE_RECIPIENTS", &cfg.GenerateRecipients)
	setN("MAX_PAYMENTS", &cfg.MaxPayments)
	setN("TARGET_CHANGE_OUTPUTS", &cfg.TargetChangeOutputs)
}

// Validate checks the file and fills derived durations. It does not load a key.
func (f File) Validate() (Runtime, error) {
	rt := Runtime{File: f}
	switch strings.ToLower(strings.TrimSpace(f.Network)) {
	case "main", "mainnet":
		rt.Mainnet = true
		rt.Network = "main"
	case "test", "testnet":
		rt.Mainnet = false
		rt.Network = "test"
	default:
		return Runtime{}, fmt.Errorf("network must be main or test (BSV). %q is not a supported network", f.Network)
	}
	if f.Broadcast && rt.Mainnet && !f.AllowMainnetBroadcast {
		return Runtime{}, errors.New("mainnet broadcast requires allow_mainnet_broadcast=true")
	}
	if f.Broadcast && !f.ControlledRecipients {
		return Runtime{}, errors.New("broadcast requires controlled_recipients=true; this process only pays keys the operator controls")
	}
	if f.TargetTPS <= 0 {
		return Runtime{}, errors.New("target_tps must be positive")
	}
	if f.MinAmount < 1 || f.MaxAmount < f.MinAmount {
		return Runtime{}, errors.New("amounts must satisfy 1 <= min_amount <= max_amount")
	}
	switch schedule.AmountDistribution(f.AmountDistribution) {
	case schedule.Uniform, schedule.LogUniform, schedule.Realistic:
		rt.Distribution = schedule.AmountDistribution(f.AmountDistribution)
	default:
		return Runtime{}, fmt.Errorf("amount_distribution must be uniform, log_uniform, or realistic")
	}
	if f.DustSat < 1 {
		return Runtime{}, errors.New("dust_sat must be positive")
	}
	if f.TargetChangeOutputs < 1 || f.TargetChangeOutputs > 8 {
		return Runtime{}, errors.New("target_change_outputs must be between 1 and 8 (toolbox MaxChangeOutputsPerTx)")
	}
	if f.MinDesiredUTXOValue < 1 {
		return Runtime{}, errors.New("min_desired_utxo_value must be positive")
	}
	if f.MinFeeSat < 0 || f.MaxFeeSat < f.MinFeeSat {
		return Runtime{}, errors.New("fee bounds must satisfy 0 <= min_fee_sat <= max_fee_sat")
	}
	if f.SatPerKB <= 0 {
		return Runtime{}, errors.New("sat_per_kb must be positive")
	}
	rt.StandardFee = fee.Standard(f.SatPerKB)
	if rt.StandardFee < f.MinFeeSat || rt.StandardFee > f.MaxFeeSat {
		return Runtime{}, fmt.Errorf("standard 1-in 2-out fee at %d sat/kb is %d sats, outside [%d,%d]", f.SatPerKB, rt.StandardFee, f.MinFeeSat, f.MaxFeeSat)
	}
	if f.DailySpendCap < 1 || f.DailyFeeCap < 1 {
		return Runtime{}, errors.New("daily caps must be positive")
	}
	if f.ReserveSat < 0 || f.MaxInFlight < 1 || f.MaxInputs < 1 || f.MinUTXOs < 1 || f.MaxUTXOs < f.MinUTXOs {
		return Runtime{}, errors.New("reserve, in-flight, inputs, and utxo bounds are inconsistent")
	}
	if !f.Broadcast && !f.ImportFunding && f.SimBalance <= f.ReserveSat+f.MaxAmount+rt.StandardFee {
		return Runtime{}, errors.New("sim_balance must cover the reserve, one maximum payment, and the fee")
	}
	var err error
	if rt.SweepEvery, err = time.ParseDuration(f.SweepInterval); err != nil || rt.SweepEvery <= 0 {
		return Runtime{}, errors.New("sweep_interval must be a positive duration")
	}
	if rt.ConfirmEvery, err = time.ParseDuration(f.ConfirmAfter); err != nil || rt.ConfirmEvery < 0 {
		return Runtime{}, errors.New("confirm_after must be a duration")
	}
	if rt.ShutdownEvery, err = time.ParseDuration(f.ShutdownTimeout); err != nil || rt.ShutdownEvery <= 0 {
		return Runtime{}, errors.New("shutdown_timeout must be a positive duration")
	}
	if f.MessageBox == "" || strings.ContainsAny(f.MessageBox, "\r\n\t ") {
		return Runtime{}, errors.New("message_box must be a single token; the PeerPay box is payment_inbox")
	}
	if !strings.HasPrefix(f.MessageBoxURL, "https://") && !strings.HasPrefix(f.MessageBoxURL, "http://") {
		return Runtime{}, errors.New("message_box_url must be an http(s) origin")
	}
	switch f.UTXOStrategy {
	case "privacy", "throughput":
	default:
		return Runtime{}, errors.New("utxo_strategy must be privacy or throughput")
	}
	if f.UTXOStrategy == "throughput" && !f.Broadcast {
		return Runtime{}, errors.New("throughput fan-out broadcasts transactions; it requires broadcast=true")
	}
	if f.Originator == "" || len(f.Originator) > 250 {
		return Runtime{}, errors.New("originator must be 1 to 250 bytes")
	}
	for _, part := range strings.Split(f.RecipientIdentityKeys, ",") {
		part = strings.TrimSpace(part)
		if part == "" {
			continue
		}
		pub, err := ec.PublicKeyFromString(part)
		if err != nil {
			return Runtime{}, errors.New("a recipient_identity_keys entry is not a compressed public key")
		}
		rt.Recipients = append(rt.Recipients, pub.ToDERHex())
	}
	if len(rt.Recipients) == 0 && f.GenerateRecipients < 1 {
		return Runtime{}, errors.New("set generate_recipients or recipient_identity_keys to at least one controlled recipient")
	}
	if len(rt.Recipients) > 0 && !f.ControlledRecipients {
		return Runtime{}, errors.New("recipient_identity_keys requires controlled_recipients=true")
	}
	return rt, nil
}

func loadKey(getenv func(string) string, mainnet bool) (*ec.PrivateKey, string, error) {
	envVal := strings.TrimSpace(getenv("SERVER_PRIVATE_KEY"))
	file := strings.TrimSpace(getenv("SERVER_PRIVATE_KEY_FILE"))
	if envVal != "" && file != "" {
		return nil, "", errors.New("set only one of SERVER_PRIVATE_KEY and SERVER_PRIVATE_KEY_FILE")
	}
	raw := envVal
	if file != "" {
		buf, err := os.ReadFile(file)
		if err != nil {
			return nil, "", errors.New("cannot read SERVER_PRIVATE_KEY_FILE")
		}
		raw = strings.TrimSpace(string(buf))
	}
	if raw == "" {
		return nil, "", errors.New("SERVER_PRIVATE_KEY or SERVER_PRIVATE_KEY_FILE is required")
	}
	key, err := parseKey(raw, mainnet)
	if err != nil {
		return nil, "", err
	}
	if key.D == nil || key.D.Sign() <= 0 {
		return nil, "", errors.New("SERVER_PRIVATE_KEY is not a usable secp256k1 scalar")
	}
	return key, key.PubKey().ToDERHex(), nil
}

func parseKey(raw string, mainnet bool) (*ec.PrivateKey, error) {
	if isHexKey(raw) {
		key, err := ec.PrivateKeyFromHex(raw)
		if err != nil || len(key.Serialize()) != 32 {
			return nil, errors.New("SERVER_PRIVATE_KEY is not a 32-byte hex private key")
		}
		return key, nil
	}
	decoded, err := base58.Decode(raw)
	if err != nil || len(decoded) < 1 {
		return nil, errors.New("SERVER_PRIVATE_KEY is not valid hex or WIF")
	}
	want := byte(wifTestNet)
	if mainnet {
		want = wifMainNet
	}
	if decoded[0] != want {
		return nil, errors.New("SERVER_PRIVATE_KEY WIF network byte does not match NETWORK")
	}
	key, err := ec.PrivateKeyFromWif(raw)
	if err != nil {
		return nil, errors.New("SERVER_PRIVATE_KEY WIF could not be parsed")
	}
	return key, nil
}

func isHexKey(s string) bool {
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
