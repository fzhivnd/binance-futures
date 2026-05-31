package domain

import "time"

type RSIDivergence struct {
	Timeframe Timeframe
	Strength  PatternStrength
}

type CandlePattern string

const (
	PatternNone             CandlePattern = "NONE"
	PatternShootingStar     CandlePattern = "SHOOTING_STAR"
	PatternBearishEngulfing CandlePattern = "BEARISH_ENGULFING"
	PatternEveningStar      CandlePattern = "EVENING_STAR"
	PatternUpperWickReject  CandlePattern = "UPPER_WICK_REJECTION"
	PatternDojiAfterPump    CandlePattern = "DOJI_AFTER_PUMP"
	PatternFailedBreakout   CandlePattern = "FAILED_BREAKOUT"
)

type PatternStrength string

const (
	StrengthStrong PatternStrength = "STRONG"
	StrengthMedium PatternStrength = "MEDIUM"
	StrengthWeak   PatternStrength = "WEAK"
)

type CandleSignal struct {
	Timeframe Timeframe
	Pattern   CandlePattern
	Strength  PatternStrength
}

type IndicatorSnapshot struct {
	Symbol    string
	Timestamp time.Time

	// RSI on 15m (setup context) and 5m (entry timing)
	RSI14_15m float64
	RSI7_5m   float64

	// OI delta: 1h for setup confirmation, 15m for recent leverage buildup
	OIDelta1h  float64
	OIDelta15m float64

	// ATR on 1h — used as pre-trade volatility filter only
	ATR14_1h float64
	ATRRatio float64

	// Volume anomaly on 5m candles — catches the surge at entry time
	VolChange5m float64
	VolumeSpike bool

	Patterns       []CandleSignal
	RSIDivergences []RSIDivergence

	MomentumLoss bool
}

type BTCContext struct {
	Trend          string
	MomentumScore  int
	Volatility     string
	RSI14_1h       float64
	PriceChange1h  float64
	PriceChange15m float64 // replaces 4h — detects breakout in the current window
	IsBreakout     bool
}
