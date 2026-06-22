package domain

type ScoringConfig struct {
	ID              int
	Name            string
	Version         int
	Weights         ScoringWeights
	Thresholds      []ScoringThreshold
	CandleWeights   []CandleWeight
	ConfidenceTiers []ConfidenceTier
	BotParams       BotParams
}

type ScoringWeights struct {
	Funding       float64
	OI            float64
	BTC           float64
	Candle        float64
	Volume        float64
	ROI           float64
	Volatility    float64
	RSIDivergence float64
}

type ScoringThreshold struct {
	Category    string
	TierOrder   int
	MinValue    *float64
	MaxValue    *float64
	Multiplier  float64
	Interpolate bool
	Condition   string
}

type CandleWeight struct {
	Type   string
	Label  string
	Weight float64
}

type ConfidenceTier struct {
	MinScore        float64
	Confidence      string
	PositionSizePct float64
}

type BotParams struct {
	MinDailyROIPct         float64
	MinVolume24hM          float64
	MaxPositions           int
	Leverage               int
	PositionSizePct        float64
	FundingMinRate         float64
	FundingMaxRate         float64
	SlPct                  float64
	TpPct                  float64
	Tp2Pct                 float64
	TrailingActivationPct  float64
	BreakevenActivationPct float64
}
