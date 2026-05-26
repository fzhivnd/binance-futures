package domain

import "time"

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

	RSI14_1h float64

	OIDelta1h float64
	OIDelta4h float64

	ATR14_1h float64
	ATRRatio float64

	VolChange1h float64
	VolumeSpike bool

	Patterns []CandleSignal

	MomentumLoss bool
}

type BTCContext struct {
	Trend         string
	MomentumScore int
	Volatility    string
	RSI14_1h      float64
	PriceChange1h float64
	PriceChange4h float64
	IsBreakout    bool
}
