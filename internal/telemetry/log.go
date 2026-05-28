package telemetry

import (
	"log/slog"
	"time"
)

func record(method string, d time.Duration, err error, args ...any) {
	ms := float64(d.Microseconds()) / 1000.0
	base := append([]any{"method", method, "latency_ms", ms}, args...)
	if err != nil {
		slog.Error("call", append(base, "error", err)...)
	} else {
		slog.Debug("call", base...)
	}
}
