package main

import (
	"context"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"time"

	"futures/internal/backtest"
	"futures/internal/config"
	"futures/internal/exchange"
	"futures/internal/scoring"
)

func main() {
	var (
		cfgPath    = flag.String("config", "config/config.yaml", "path to config file")
		startStr   = flag.String("start", "", "start date (YYYY-MM-DD)")
		endStr     = flag.String("end", "", "end date (YYYY-MM-DD)")
		balance    = flag.Float64("balance", 1000, "initial balance (USDT)")
		symbolsStr = flag.String("symbols", "", "comma-separated symbols, empty = use config default")
		outputDir  = flag.String("output", ".", "output directory for trades.csv")
	)
	flag.Parse()

	cfg, err := config.Load(*cfgPath)
	if err != nil {
		slog.Error("load config", "error", err)
		os.Exit(1)
	}

	start, err := time.Parse("2006-01-02", *startStr)
	if err != nil {
		slog.Error("invalid start date", "error", err)
		os.Exit(1)
	}
	end, err := time.Parse("2006-01-02", *endStr)
	if err != nil {
		slog.Error("invalid end date", "error", err)
		os.Exit(1)
	}

	var symbols []string
	if *symbolsStr != "" {
		symbols = strings.Split(*symbolsStr, ",")
	} else {
		slog.Error("--symbols is required (e.g. 1000PEPEUSDT,DOGEUSDT)")
		os.Exit(1)
	}

	weights := scoring.WeightConfig{
		Funding:    cfg.Scoring.Weights.Funding,
		OI:         cfg.Scoring.Weights.OI,
		BTC:        cfg.Scoring.Weights.BTC,
		Candle:     cfg.Scoring.Weights.Candle,
		Volume:     cfg.Scoring.Weights.Volume,
		ROI:        cfg.Scoring.Weights.ROI,
		Volatility: cfg.Scoring.Weights.Volatility,
	}

	btCfg := backtest.Config{
		StartDate:      start,
		EndDate:        end,
		Symbols:        symbols,
		InitialBalance: *balance,
		Leverage:       cfg.Trading.Leverage,
		Weights:        weights,
		MinScore:       cfg.Scoring.MinScore,
		MaxATRRatio:    cfg.Risk.MaxATRRatio,
	}

	client := exchange.NewBinanceClient(cfg.Binance.APIKey, cfg.Binance.APISecret, cfg.Binance.BaseURL)
	loader := backtest.NewDataLoader(client)
	runner := backtest.NewRunner(btCfg, loader)

	ctx := context.Background()
	result, err := runner.Run(ctx)
	if err != nil {
		slog.Error("backtest failed", "error", err)
		os.Exit(1)
	}

	result.PrintSummary()

	csvPath := fmt.Sprintf("%s/trades.csv", *outputDir)
	if err := result.WriteCSV(csvPath); err != nil {
		slog.Error("write csv", "error", err)
	} else {
		slog.Info("trades exported", "path", csvPath)
	}
}
