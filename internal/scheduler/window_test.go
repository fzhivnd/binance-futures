package scheduler

import (
	"testing"
	"time"
)

func utc(hour, min, sec int) time.Time {
	return time.Date(2024, 1, 15, hour, min, sec, 0, time.UTC)
}

func TestNextFundingTime(t *testing.T) {
	tests := []struct {
		now      time.Time
		wantHour int
		wantDay  int
	}{
		{utc(0, 0, 0), 4, 15},    // exactly on 00:00 → next funding is 04:00
		{utc(3, 59, 59), 4, 15},  // just before 04:00 → next is 04:00
		{utc(4, 0, 0), 8, 15},    // exactly 04:00:00 → next is 08:00
		{utc(4, 0, 1), 4, 15},    // 04:00:01 → currentHour==4 & second>0 → returns 04:00 same day
		{utc(4, 1, 0), 4, 15},    // 04:01:00 → currentHour==4 & minute>0 → returns 04:00 same day
		{utc(21, 0, 0), 0, 16},   // 21:00:00 exactly, past all windows → wraps to midnight next day
		{utc(23, 59, 59), 0, 16}, // late night → wraps to midnight next day
	}

	for _, tt := range tests {
		got := NextFundingTime(tt.now)
		if got.Hour() != tt.wantHour || got.Day() != tt.wantDay {
			t.Errorf("NextFundingTime(%v) = %v, want day=%d hour=%d",
				tt.now, got, tt.wantDay, tt.wantHour)
		}
		if got.Minute() != 0 || got.Second() != 0 {
			t.Errorf("NextFundingTime result not on the minute: %v", got)
		}
	}
}

func TestCurrentWindow(t *testing.T) {
	windowStart := 30 // minutes before funding

	// funding at 04:00; reference times relative to it
	funding := time.Date(2024, 1, 15, 4, 0, 0, 0, time.UTC)

	tests := []struct {
		name string
		now  time.Time
		want WindowType
	}{
		{"well before window", funding.Add(-45 * time.Minute), WindowNone},
		{"just entering frontrun", funding.Add(-29 * time.Minute), WindowFrontrun},
		{"mid frontrun", funding.Add(-20 * time.Minute), WindowFrontrun},
		{"last 5 min boundary", funding.Add(-5 * time.Minute), WindowLastMinute},
		{"last minute", funding.Add(-1 * time.Minute), WindowLastMinute},
		{"1s before funding", funding.Add(-time.Second), WindowLastMinute},
		// At exactly 04:00:00, NextFundingTime returns 08:00 (4h away) → WindowNone.
		// WindowAfter triggers when until ∈ (-1min, 0], i.e. within 1 min *after* funding.
		// Because NextFundingTime returns the same hour when second/minute>0, "after" is
		// represented as a negative until relative to that same-hour result.
		{"exactly at funding", funding, WindowNone},
		{"30s after funding", funding.Add(30 * time.Second), WindowAfter},
		{"59s after funding", funding.Add(59 * time.Second), WindowAfter},
		{"1min after funding", funding.Add(time.Minute), WindowNone},
	}

	for _, tt := range tests {
		got := CurrentWindow(tt.now, windowStart)
		if got != tt.want {
			t.Errorf("%s: CurrentWindow = %q, want %q", tt.name, got, tt.want)
		}
	}
}

func TestWindowType_ToEntryMode(t *testing.T) {
	cases := []struct {
		w    WindowType
		want string
	}{
		{WindowFrontrun, "FRONTRUN"},
		{WindowLastMinute, "LAST_MINUTE"},
		{WindowAfter, "AFTER"},
		{WindowNone, ""},
	}
	for _, c := range cases {
		if got := c.w.ToEntryMode(); got != c.want {
			t.Errorf("%q.ToEntryMode() = %q, want %q", c.w, got, c.want)
		}
	}
}
