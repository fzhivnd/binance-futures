package domain

type ScoredCandidate struct {
	Candidate       Candidate
	Indicators      *IndicatorSnapshot
	BTCContext      *BTCContext
	CompositeScore  float64
	Breakdown       ScoreBreakdown
	Confidence      string
	PositionSizePct float64
}

type ScoreBreakdown struct {
	FundingScore         float64
	OIScore              float64
	OI15mScore           float64
	BTCScore             float64
	CandleScore          float64
	VolumeScore          float64
	ROIScore             float64
	VolatilityScore      float64
	RSIDivergenceScore   float64
	BullishCandlePenalty float64 // negative: subtracted from composite
	MomentumPenalty      float64 // negative: subtracted from composite
}
