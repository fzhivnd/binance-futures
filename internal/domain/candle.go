package domain

import "time"

type Timeframe string

const (
	Timeframe5m  Timeframe = "5m"
	Timeframe15m Timeframe = "15m"
	Timeframe30m Timeframe = "30m"
	Timeframe1h  Timeframe = "1h"
	Timeframe4h  Timeframe = "4h"
	Timeframe1d  Timeframe = "1d"
)

var AllTimeframes = []Timeframe{Timeframe5m, Timeframe15m, Timeframe30m, Timeframe1h, Timeframe4h, Timeframe1d}

type Candle struct {
	Symbol    string
	Timeframe Timeframe
	OpenTime  time.Time
	Open      float64
	High      float64
	Low       float64
	Close     float64
	Volume    float64
	CloseTime time.Time
	IsClosed  bool
}
