package intent

import (
	"time"

	"futures/internal/domain"
)

type IntentStatus string

const (
	IntentPending  IntentStatus = "PENDING"
	IntentFired    IntentStatus = "FIRED"
	IntentExpired  IntentStatus = "EXPIRED"
	IntentRejected IntentStatus = "REJECTED"
)

type TradeIntent struct {
	Symbol          string
	Candidate       *domain.ScoredCandidate
	Decision        *domain.LLMDecision
	TargetEntryMode domain.EntryMode
	CreatedAt       time.Time
	ExpiresAt       time.Time
	Status          IntentStatus
}

func (i *TradeIntent) IsExpired(now time.Time) bool {
	return now.After(i.ExpiresAt)
}
