package domain

import "time"

type FundingRate struct {
	Symbol      string
	Rate        float64
	NextFunding time.Time
	MarkPrice   float64
	UpdatedAt   time.Time
}
