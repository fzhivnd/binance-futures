package scheduler

import "time"

type WindowType string

const (
	WindowFrontrun   WindowType = "FRONTRUN"
	WindowLastMinute WindowType = "LAST_MINUTE"
	WindowAfter      WindowType = "AFTER"
	WindowNone       WindowType = ""
)

func (w WindowType) ToEntryMode() string {
	switch w {
	case WindowFrontrun:
		return "FRONTRUN"
	case WindowLastMinute:
		return "LAST_MINUTE"
	case WindowAfter:
		return "AFTER"
	default:
		return ""
	}
}

// NextFundingTime returns the next Binance funding settlement time after now.
func NextFundingTime(now time.Time) time.Time {
	utc := now.UTC()
	fundingHours := []int{0, 4, 8, 12, 16, 20}
	currentHour := utc.Hour()

	for _, h := range fundingHours {
		if currentHour < h ||
			(currentHour == h && (utc.Minute() > 0 || utc.Second() > 0 || utc.Nanosecond() > 0)) {
			return time.Date(utc.Year(), utc.Month(), utc.Day(), h, 0, 0, 0, time.UTC)
		}
	}

	tomorrow := utc.AddDate(0, 0, 1)
	return time.Date(tomorrow.Year(), tomorrow.Month(), tomorrow.Day(), 0, 0, 0, 0, time.UTC)
}

// CurrentWindow returns which trading window we're in, or WindowNone.
func CurrentWindow(now time.Time, windowStartMinutes int) WindowType {
	next := NextFundingTime(now)
	until := next.Sub(now)

	windowStart := time.Duration(windowStartMinutes) * time.Minute
	switch {
	case until <= 0 && until > -1*time.Minute:
		return WindowAfter
	case until > 0 && until <= 6*time.Minute:
		return WindowLastMinute
	case until > 6*time.Minute && until <= windowStart:
		return WindowFrontrun
	default:
		return WindowNone
	}
}

// NextWindowBounds returns the open (T-windowStartMinutes) and close (T+5m)
// times of the funding window that is either currently active or next
// upcoming, along with the settlement time it's anchored to. Market-data
// subscriptions are opened at `open` and torn down at `close`, so every
// symbol enters a window with freshly backfilled candles and never carries
// data across funding cycles.
//
// Guarantees close > now (or ==) for whatever `now` is passed in: if the
// most recently settled window has already closed, this steps forward to
// the next one instead of returning a bound in the past.
func NextWindowBounds(now time.Time, windowStartMinutes int) (open, close, settlement time.Time) {
	settlement = NextFundingTime(now)
	close = settlement.Add(5 * time.Minute)
	for now.After(close) {
		settlement = NextFundingTime(settlement.Add(time.Hour))
		close = settlement.Add(5 * time.Minute)
	}
	open = settlement.Add(-time.Duration(windowStartMinutes) * time.Minute)
	return open, close, settlement
}
