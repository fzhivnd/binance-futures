package domain

import (
	"time"

	"github.com/google/uuid"
)

type TradeAction string

const (
	ActionTrade TradeAction = "TRADE"
	ActionSkip  TradeAction = "SKIP"
)

type TradeMemory struct {
	ID        uuid.UUID
	TradeID   *uuid.UUID // nil for skip decisions
	Symbol    string
	Action    TradeAction
	CreatedAt time.Time

	// Setup conditions at decision time
	FundingRate     float64
	DailyROI        float64
	OIDelta1h       float64
	OIDelta15m      float64
	RSI14_15m       float64
	RSI7_5m         float64
	ATRRatio        float64
	VolChange5m     float64
	VolumeSpike     bool
	MomentumLoss    bool
	CandlePatterns  string // serialized: "1h:SHOOTING_STAR(STRONG) 30m:..."
	CompositeScore  float64
	EntryMode       string
	MinutesToSettle int

	// BTC context
	BTCTrend    string
	BTCMomentum int
	BTCBreakout bool
	BTCRSI      float64
	BTCChange1h float64

	// Outcome (filled after trade closes, or retroactively for skips)
	Outcome     string  // "WIN" | "PARTIAL_WIN" | "LOSS" | "FORCE_SL" | "BREAKEVEN" | "SKIP_VALIDATED" | "SKIP_MISSED"
	ProfitPct   float64 // actual or hypothetical for skips
	HoldMinutes int     // duration from entry to exit

	// LLM-generated lesson
	Lesson string

	// Metadata for filtered search
	BTCRegime     string // "bullish" | "neutral" | "bearish"
	FundingBucket int    // 1: -0.2~-0.5, 2: -0.5~-1.0, 3: -1.0~-2.0
}

// SimilarTrade is the compact form injected into LLM prompts.
type SimilarTrade struct {
	Outcome     string
	ProfitPct   float64
	Similarity  float64
	Lesson      string
	DaysAgo     int
	EntryMode   string
	FundingRate float64 // funding rate at decision time (e.g. -0.8)
}
