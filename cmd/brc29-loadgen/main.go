package main

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"
	"time"

	"github.com/sirdeggen/bsv-noise/internal/config"
	"github.com/sirdeggen/bsv-noise/internal/engine"
	"github.com/sirdeggen/bsv-noise/internal/httpserver"
	"github.com/sirdeggen/bsv-noise/internal/live"
	"github.com/sirdeggen/bsv-noise/internal/messagebox"
	"github.com/sirdeggen/bsv-noise/internal/recipients"
	"github.com/sirdeggen/bsv-noise/internal/redact"
	"github.com/sirdeggen/bsv-noise/internal/store"
	"github.com/sirdeggen/bsv-noise/internal/walletlive"
)

func main() {
	if len(os.Args) > 1 && os.Args[1] == "health" {
		os.Exit(health())
	}
	if err := serve(); err != nil {
		slog.Error("stopped", "error", err.Error())
		os.Exit(1)
	}
}

func health() int {
	addr := os.Getenv("LISTEN")
	if addr == "" {
		addr = "127.0.0.1:8741"
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, "http://"+addr+"/healthz", nil)
	if err != nil {
		fmt.Fprintln(os.Stderr, "health check failed")
		return 1
	}
	resp, err := http.DefaultClient.Do(req)
	if err != nil {
		fmt.Fprintln(os.Stderr, "health check failed")
		return 1
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		fmt.Fprintln(os.Stderr, "not healthy")
		return 1
	}
	var doc struct {
		Synthetic bool `json:"synthetic"`
	}
	_ = json.Unmarshal(body, &doc)
	if !doc.Synthetic {
		fmt.Fprintln(os.Stderr, "health document is missing the synthetic marker")
		return 1
	}
	return 0
}

func serve() error {
	level := slog.LevelInfo
	if os.Getenv("LOG_LEVEL") == "debug" {
		level = slog.LevelDebug
	}
	log := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{Level: level}))
	slog.SetDefault(log)

	rt, err := config.Load("", nil)
	if err != nil {
		return err
	}
	secrets := redact.New(rt.Private.Hex(), rt.ARCToken)

	if err := os.MkdirAll(rt.DataDir, 0o700); err != nil {
		return err
	}
	pubs, err := recipients.Ensure(rt.DataDir, rt.GenerateRecipients, rt.Recipients)
	if err != nil {
		return err
	}
	st, err := store.Open(filepath.Join(rt.DataDir, "loadgen.sqlite"), rt.ExternalEffects)
	if err != nil {
		return err
	}
	defer st.Close()
	if !rt.ExternalEffects {
		if err := st.EnsureGenesis("sim:genesis:0", rt.SimBalance); err != nil {
			return err
		}
	}

	var (
		w   *walletlive.Wallet
		box *messagebox.Client
		snd *live.Sender
	)
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if rt.ExternalEffects {
		w, err = walletlive.Open(ctx, rt.Network, rt.Mainnet, rt.Private, rt.Identity, rt.DataDir, rt.Originator, rt.ARCURL, rt.ARCToken, rt.SatPerKB, rt.UTXOStrategy == "throughput", log)
		if err != nil {
			return fmt.Errorf("%s", secrets.String(err.Error()))
		}
		defer w.Close()
		snd = &live.Sender{
			Wallet:         w,
			Fuel:           w,
			Originator:     rt.Originator,
			Throughput:     rt.UTXOStrategy == "throughput",
			TargetChange:   rt.TargetChangeOutputs,
			MinDesiredUTXO: uint64(rt.MinDesiredUTXOValue),
			DustSat:        uint64(rt.DustSat),
			SatPerKB:       rt.SatPerKB,
		}
		if err := w.CleanupStaleActions(ctx); err != nil {
			return fmt.Errorf("cleanup stale wallet actions: %s", secrets.String(err.Error()))
		}
		if err := w.PrepareBasket(ctx, rt.TargetChangeOutputs, uint64(rt.MinDesiredUTXOValue)); err != nil {
			return fmt.Errorf("prepare change basket: %s", secrets.String(err.Error()))
		}
		if rt.ImportFunding {
			box = &messagebox.Client{
				BaseURL:    rt.MessageBoxURL,
				Box:        rt.MessageBox,
				Poster:     messagebox.NewAuthPoster(w.SDK()),
				Wallet:     w.SDK(),
				Originator: rt.Originator,
				Log:        log,
			}
			log.Warn("import_funding calls InternalizeAction. An unproven funding transaction can be rebroadcast by the wallet. This is not a dry run.")
		}
		if rt.Broadcast {
			log.Warn("broadcast is on. Payments are real BSV transactions to controlled recipients. They are self-controlled transfers, not evidence of commerce.")
		}
	}

	eng, err := engine.New(engine.Options{
		Runtime: rt, Store: st, Recipients: pubs, Box: box, Live: snd, Watcher: w, Log: log,
	})
	if err != nil {
		return err
	}
	if httpserver.WarnOpen(rt.Listen, rt.MetricsBearer) {
		log.Warn("metrics and readiness have no token and listen beyond loopback")
	}
	if httpserver.WarnOpen(rt.Listen, rt.ConsoleBearer) {
		log.Warn("console and /api/stats have no token and listen beyond loopback")
	}
	srv, err := httpserver.Listen(rt.Listen, (&httpserver.Server{
		Status:       eng.Status,
		Metrics:      eng.Metrics,
		Stats:        eng.Stats,
		Token:        rt.MetricsBearer,
		ConsoleToken: rt.ConsoleBearer,
	}).Handler())
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	defer func() {
		shctx, cancel := context.WithTimeout(context.Background(), rt.ShutdownEvery)
		defer cancel()
		_ = srv.Shutdown(shctx)
	}()

	mode := "dry-run"
	if rt.Broadcast {
		mode = "broadcast"
	} else if rt.ImportFunding {
		mode = "import-only"
	}
	log.Info("bsv noise service",
		"mode", mode,
		"network", rt.Network,
		"identity_key", rt.Identity,
		"message_box", rt.MessageBox,
		"message_box_url", rt.MessageBoxURL,
		"listen", rt.Listen,
		"target_tps", rt.TargetTPS,
		"recipients", len(pubs),
		"synthetic", true,
		"wallet_toolbox", "v0.191.4",
	)
	if err := eng.Run(ctx); err != nil {
		return fmt.Errorf("%s", secrets.String(err.Error()))
	}
	return nil
}
