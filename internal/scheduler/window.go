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
	case until > 0 && until <= 10*time.Minute:
		return WindowLastMinute
	case until > 10*time.Minute && until <= windowStart:
		return WindowFrontrun
	default:
		return WindowNone
	}
}
