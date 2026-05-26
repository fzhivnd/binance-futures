package domain

type Candidate struct {
	Symbol      string
	FundingRate float64
	MarkPrice   float64
	DailyROI    float64
	ROI1D       float64 // price % change over last closed 1d candle
	ROI4H       float64 // price % change over last closed 4h candle
	ROI1H       float64 // price % change over last closed 1h candle
	Volume24h   float64 // 24h trading volume in quote asset (USDT)
	Score       float64
}
