package llm

// ForceSLRequest is the input for force stop-loss LLM evaluation (Phase 5).
type ForceSLRequest struct {
	Symbol             string             `json:"symbol"`
	EntryPrice         float64            `json:"entry_price"`
	CurrentPrice       float64            `json:"current_price"`
	UnrealizedPnlPct   float64            `json:"unrealized_pnl_pct"` // at 20x leverage
	HoldMinutes        int                `json:"hold_minutes"`
	HardSLPrice        float64            `json:"hard_sl_price"`
	HardSLDistancePct  float64            `json:"hard_sl_distance_pct"`
	EntryMode          string             `json:"entry_mode"`
	OriginalConfidence int                `json:"original_confidence"`
	EntryReasons       []string           `json:"entry_reasons"`
	PriceAction        ForceSLPriceAction `json:"price_action"`
	BTCContext         LLMBTCContext      `json:"btc_context"`
}

type ForceSLPriceAction struct {
	HighSinceEntry   float64 `json:"high_since_entry_pct"`  // max adverse move (price went up = bad for short)
	LowSinceEntry    float64 `json:"low_since_entry_pct"`   // max favorable move (price went down = good for short)
	CurrentTrend5m   string  `json:"current_trend_5m"`      // "up" | "down" | "sideways"
	MomentumShift    bool    `json:"momentum_shift"`        // reversal forming against us
	VolumeIncreasing bool    `json:"volume_increasing"`     // buying pressure building
}

type ForceSLResponse struct {
	Action string `json:"action"` // "HOLD" | "FORCE_CLOSE"
	Reason string `json:"reason"`
}

type LLMRequest struct {
	Timestamp           int64             `json:"timestamp"`
	MinutesToSettlement int               `json:"minutes_to_settlement"`
	BTCContext          LLMBTCContext     `json:"btc_context"`
	Candidates          []LLMCandidate    `json:"candidates"`
	SimilarTrades       []LLMSimilarTrade `json:"similar_past_trades,omitempty"` // Phase 4
}

type LLMSimilarTrade struct {
	Outcome    string  `json:"outcome"`
	ProfitPct  float64 `json:"profit_pct"`
	Similarity float64 `json:"similarity"`
	Lesson     string  `json:"lesson"`
	DaysAgo    int     `json:"days_ago"`
}

type LLMBTCContext struct {
	Trend         string  `json:"trend"`
	MomentumScore int     `json:"momentum_score"`
	Volatility    string  `json:"volatility"`
	IsBreakout    bool    `json:"is_breakout"`
	RSI           float64 `json:"rsi"`
	PriceChange1h float64 `json:"price_change_1h_pct"`
}

type LLMCandidate struct {
	Symbol           string          `json:"symbol"`
	FundingRate      float64         `json:"funding_rate_pct"`
	DailyROI         float64         `json:"daily_roi_pct"`
	ProjectedTP1Pct  float64         `json:"projected_tp1_pct"` // base TP + funding fee for pre-settlement entry
	CompositeScore   float64         `json:"composite_score"`
	ScoreBreakdown   LLMBreakdown    `json:"score_breakdown"`
	RSI14_15m        float64         `json:"rsi_14_15m"`
	RSI7_5m          float64         `json:"rsi_7_5m"`
	OIDelta1h        float64         `json:"oi_delta_1h_pct"`
	OIDelta15m       float64         `json:"oi_delta_15m_pct"`
	ATRRatio         float64         `json:"atr_ratio"`
	VolChange5m      float64         `json:"vol_change_5m_pct"`
	VolumeSpikeFlag  bool            `json:"volume_spike"`
	MomentumLoss     bool            `json:"momentum_loss"`
	CandlePatterns   []LLMCandleInfo `json:"candle_patterns"`
}

type LLMBreakdown struct {
	Funding    float64 `json:"funding"`
	OI         float64 `json:"oi"`
	BTC        float64 `json:"btc"`
	Candle     float64 `json:"candle"`
	Volume     float64 `json:"volume"`
	ROI        float64 `json:"roi"`
	Volatility float64 `json:"volatility"`
}

type LLMCandleInfo struct {
	Timeframe string `json:"timeframe"`
	Pattern   string `json:"pattern"`
	Strength  string `json:"strength"`
}

type LLMResponse struct {
	Action       string   `json:"action"`
	Symbol       string   `json:"symbol"`
	Confidence   int      `json:"confidence"`
	EntryMode    string   `json:"entry_mode"`
	EntryReasons []string `json:"entry_reasons"`
	Warnings     []string `json:"warnings"`
	SkipReason   string   `json:"skip_reason"`
}
