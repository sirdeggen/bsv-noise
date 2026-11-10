// Package walletlive opens a go-wallet-toolbox wallet on local SQLite.
//
// The same SERVER_PRIVATE_KEY is the Message Box identity, the wallet user,
// and the local storage operator. Funding is internalized into this wallet.
// Spending uses CreateAction on this wallet. A process-wide mutex keeps those
// two paths from selecting inputs at the same time.
package walletlive

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"
	"time"

	ec "github.com/bsv-blockchain/go-sdk/primitives/ec"
	sdk "github.com/bsv-blockchain/go-sdk/wallet"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/defs"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/monitor"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/services"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/storage"
	toolboxwallet "github.com/bsv-blockchain/go-wallet-toolbox/pkg/wallet"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wdk"
	"github.com/bsv-blockchain/go-wallet-toolbox/pkg/wdk/primitives"
	"github.com/sirdeggen/bsv-noise/internal/arcadestatus"
)

// gorillaPoolArcadeURL is the GorillaPool Arcade host (Arcade, not classic ARC).
// go-wallet-toolbox v0.191.4 has a single Arcade primary and no public hook to
// add a second Arcade-type failover to the broadcast router; the arcade client
// itself lives in an internal package. This URL is documented for an upstream
// toolbox change and is not pointed at the classic ARC client.
const gorillaPoolArcadeURL = "https://arcade.gorillapool.io"

// Wallet is the process spending wallet.
type Wallet struct {
	mu            sync.Mutex
	inner         *toolboxwallet.Wallet
	provider      *storage.Provider
	monitor       *monitor.Daemon
	services      *services.WalletServices
	svcCfg        defs.WalletServices
	originator    string
	identity      string
	throughput    bool
	netEvents     chan arcadestatus.Event
	statusClient  *arcadestatus.Client
	log           *slog.Logger
	lastSweep     time.Time
	basketDesired int64
	basketMin     uint64
	maxChangeSet  int
}

// Open creates the SQLite wallet and starts the toolbox storage monitor.
// Confirmation depends on that monitor: Arcade SSE is the preferred merkle-proof
// path, and CheckForProofs is the poll fallback. Without it, actions stay
// "unproven" even after they are mined.
func Open(ctx context.Context, network string, mainnet bool, key *ec.PrivateKey, identity, dataDir, originator, arcURL, arcToken string, satPerKB int64, throughput bool, log *slog.Logger) (*Wallet, error) {
	if key == nil {
		return nil, fmt.Errorf("private key is required")
	}
	if log == nil {
		log = slog.Default()
	}
	chain := defs.NetworkTestnet
	if mainnet {
		chain = defs.NetworkMainnet
	}
	svcCfg, err := servicesConfig(chain, identity, arcURL, arcToken, log)
	if err != nil {
		return nil, err
	}
	walletServices := services.New(log, svcCfg)
	dbCfg := defs.DefaultDBConfig()
	walletFile := filepath.Join(dataDir, "wallet.sqlite")
	// One connection serializes toolbox background broadcast with our
	// CreateAction writers. A longer busy_timeout lets the waiting
	// connection retry instead of returning "database is locked".
	dbCfg.MaxOpenConnections = 1
	dbCfg.SQLite.ConnectionString = walletFile + "?_busy_timeout=30000&_journal_mode=WAL"
	opts := []storage.ProviderOption{
		storage.WithDBConfig(dbCfg),
		storage.WithLogger(log),
		storage.WithBackgroundBroadcasterContext(ctx),
		storage.WithFeeModel(defs.FeeModel{Type: defs.SatPerKB, Value: satPerKB}),
	}
	if throughput {
		mgmt := defs.DefaultUTXOManagement()
		mgmt.Strategy = defs.StrategyThroughput
		opts = append(opts, storage.WithUTXOManagement(mgmt))
	}
	provider, err := storage.NewGORMProvider(chain, walletServices, opts...)
	if err != nil {
		return nil, fmt.Errorf("create wallet storage: %w", err)
	}
	if _, err := provider.Migrate(ctx, "brc29-synthetic-load", identity); err != nil {
		provider.Stop(context.Background())
		return nil, fmt.Errorf("migrate wallet storage: %w", err)
	}
	if err := os.Chmod(walletFile, 0o600); err != nil && !os.IsNotExist(err) {
		provider.Stop(context.Background())
		return nil, err
	}
	if err := walletServices.StartBackgroundServices(ctx); err != nil {
		provider.Stop(context.Background())
		return nil, fmt.Errorf("start wallet background services: %w", err)
	}
	inner, err := toolboxwallet.New(chain, key, provider, toolboxwallet.WithLogger(log), toolboxwallet.WithServices(walletServices))
	if err != nil {
		provider.Stop(context.Background())
		return nil, fmt.Errorf("create wallet: %w", err)
	}
	events := make(chan arcadestatus.Event, 256)
	w := &Wallet{
		inner:      inner,
		provider:   provider,
		services:   walletServices,
		svcCfg:     svcCfg,
		originator: originator,
		identity:   identity,
		throughput: throughput,
		log:        log,
		netEvents:  events,
		statusClient: &arcadestatus.Client{
			BaseURL:      svcCfg.Arcade.URL,
			FallbackURLs: []string{gorillaPoolArcadeURL},
		},
	}
	if err := w.startMonitor(ctx, log); err != nil {
		w.Close()
		return nil, err
	}
	return w, nil
}

// servicesConfig builds toolbox service settings. TAAL ARC is disabled (it is
// shutting down). GorillaPool's classic ARC slot is also disabled because that
// host now runs Arcade, and the ARC client is not Arcade-compatible. Primary
// Arcade stays at the toolbox default (arcade-v2-us-1) and WhatsOnChain is
// left as a broadcast failover and header/proof poll source. A callback token
// is required so Arcade can scope the SSE /events stream to this instance.
func servicesConfig(chain defs.BSVNetwork, identity, arcURL, arcToken string, log *slog.Logger) (defs.WalletServices, error) {
	cfg := defs.DefaultServicesConfig(chain)
	if arcURL != "" || arcToken != "" {
		if log != nil {
			log.Warn("ARC_URL / ARC_TOKEN are ignored; TAAL ARC is disabled")
		}
	}
	cfg.ArcConfig.Enabled = false
	cfg.ArcGorillaPoolConfig.Enabled = false
	if cfg.Arcade.Enabled {
		if err := cfg.Arcade.Validate(); err != nil {
			return defs.WalletServices{}, fmt.Errorf("arcade config: %w", err)
		}
		if identity != "" && cfg.Arcade.CallbackToken == "" {
			cfg.Arcade.CallbackToken = wdk.DeriveArcadeCallbackToken(identity)
		}
	}
	return cfg, nil
}

func (w *Wallet) startMonitor(ctx context.Context, log *slog.Logger) error {
	if w.provider == nil || w.provider.Database == nil || w.provider.Database.DB == nil {
		return fmt.Errorf("wallet storage has no database for the monitor")
	}
	var monitorOpts []monitor.DaemonEventOption
	if w.svcCfg.Arcade.Enabled && w.services != nil {
		// Preferred proof path: Arcade SSE /events, scoped by the callback token
		// sent on broadcast. CheckForProofs remains the poll fallback.
		// Tee the same frames to the loadgen so MaxInFlight can clear on
		// SEEN_ON_NETWORK without a second SSE subscription.
		monitorOpts = append(monitorOpts, monitor.WithBroadcastEventStream(&eventTee{
			inner: w.services,
			out:   w.netEvents,
		}))
	}
	daemon, err := monitor.NewDaemonWithGORMLocker(ctx, log, w.provider, w.provider.Database.DB, monitorOpts...)
	if err != nil {
		return fmt.Errorf("create storage monitor: %w", err)
	}
	cfg := defs.DefaultMonitorConfig()
	// Existing unproven-but-mined payments will not appear on a newly scoped
	// SSE stream (they were broadcast without a callback token). Start the
	// merkle-path poll immediately so those rows can complete.
	cfg.Tasks.CheckForProofs.StartImmediately = true
	if err := daemon.Start(ctx, cfg.Tasks.EnabledTasks()); err != nil {
		_ = daemon.Stop(context.Background())
		return fmt.Errorf("start storage monitor: %w", err)
	}
	w.monitor = daemon
	log.Info("wallet storage monitor started",
		"arcade", w.svcCfg.Arcade.Enabled,
		"arcade_url", w.svcCfg.Arcade.URL,
		"sse", w.svcCfg.Arcade.Enabled,
		"taal_arc", w.svcCfg.ArcConfig.Enabled,
		"gorillapool_arc", w.svcCfg.ArcGorillaPoolConfig.Enabled,
		"gorillapool_arcade_unwired", gorillaPoolArcadeURL,
	)
	return nil
}

// Close stops the monitor and the storage background tasks.
func (w *Wallet) Close() {
	if w == nil {
		return
	}
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.monitor != nil {
		_ = w.monitor.Stop(context.Background())
		w.monitor = nil
	}
	if w.inner != nil {
		w.inner.Close()
	}
	if w.provider != nil {
		w.provider.Stop(context.Background())
	}
}

func (w *Wallet) Balance(ctx context.Context) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.inner.Balance(ctx)
}

func (w *Wallet) CreateAction(ctx context.Context, args sdk.CreateActionArgs, originator string) (*sdk.CreateActionResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if originator == "" {
		originator = w.originator
	}
	return w.inner.CreateAction(ctx, args, originator)
}

func (w *Wallet) InternalizeAction(ctx context.Context, args sdk.InternalizeActionArgs, originator string) (*sdk.InternalizeActionResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if originator == "" {
		originator = w.originator
	}
	return w.inner.InternalizeAction(ctx, args, originator)
}

func (w *Wallet) ListOutputs(ctx context.Context, args sdk.ListOutputsArgs, originator string) (*sdk.ListOutputsResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if originator == "" {
		originator = w.originator
	}
	return w.inner.ListOutputs(ctx, args, originator)
}

func (w *Wallet) ListActions(ctx context.Context, args sdk.ListActionsArgs, originator string) (*sdk.ListActionsResult, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	if originator == "" {
		originator = w.originator
	}
	return w.inner.ListActions(ctx, args, originator)
}

// FanOutFuel asks the toolbox for one denominated fan-out. It broadcasts when
// the storage strategy is throughput. Privacy mode returns the toolbox error.
func (w *Wallet) FanOutFuel(ctx context.Context, count int, each uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if count < 1 {
		count = 1
	}
	_, err := w.inner.FanOutFuel(ctx, wdk.ShapedChange{
		Count:    uint64(count),
		Satoshis: primitives.SatoshiValue(each),
		Basket:   primitives.NewIdentifier("fuel"),
	}, w.originator)
	if err != nil {
		return fmt.Errorf("fan-out: %w", err)
	}
	return nil
}

// SDK returns the wallet as a BRC-31 signer for Message Box.
func (w *Wallet) SDK() sdk.Interface { return w.inner }
