package dashboard

import (
	"context"
	"strconv"
	"time"

	"github.com/jackc/pgx/v5/pgxpool"
)

type TradeRow struct {
	ID            string
	Symbol        string
	Side          string
	EntryMode     string
	Leverage      int
	Confidence    int
	FundingRate   float64
	DailyROI      float64
	EntryPrice    float64
	Quantity      float64
	AvgClosePrice *float64
	PnL           *float64
	Result        *string
	CloseReason   *string
	IsPaper       bool
	LLMConfidence *int
	CreatedAt     time.Time
	ClosedAt      *time.Time
}

type SummaryRow struct {
	TradeDate  time.Time
	TradeCount int
	WinCount   int
	LossCount  int
	WinRate    float64
	TotalPnL   float64
	IsPaper    bool
}

type StatsRow struct {
	TotalTrades int
	WinCount    int
	LossCount   int
	WinRate     float64
	TotalPnL    float64
	BestDayPnL  float64
	IsPaper     bool
}

type Queries struct {
	pool *pgxpool.Pool
}

func NewQueries(pool *pgxpool.Pool) *Queries {
	return &Queries{pool: pool}
}

func (q *Queries) ListTrades(ctx context.Context, from, to time.Time, isPaper *bool, result *string, limit int) ([]TradeRow, error) {
	args := []any{from, to, limit}
	filter := ""
	argIdx := 4

	if isPaper != nil {
		filter += " AND is_paper = $" + itoa(argIdx)
		args = append(args, *isPaper)
		argIdx++
	}
	if result != nil && *result != "" {
		filter += " AND result = $" + itoa(argIdx)
		args = append(args, *result)
	}

	rows, err := q.pool.Query(ctx, `
		SELECT id, symbol, side, entry_mode, leverage, confidence,
		       funding_rate, daily_roi, entry_price, quantity, avg_close_price,
		       pnl, result, close_reason, is_paper, llm_confidence,
		       created_at, closed_at
		FROM trades
		WHERE created_at >= $1 AND created_at < $2
		`+filter+`
		ORDER BY created_at DESC
		LIMIT $3
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var trades []TradeRow
	for rows.Next() {
		var t TradeRow
		if err := rows.Scan(
			&t.ID, &t.Symbol, &t.Side, &t.EntryMode, &t.Leverage, &t.Confidence,
			&t.FundingRate, &t.DailyROI, &t.EntryPrice, &t.Quantity, &t.AvgClosePrice,
			&t.PnL, &t.Result, &t.CloseReason, &t.IsPaper, &t.LLMConfidence,
			&t.CreatedAt, &t.ClosedAt,
		); err != nil {
			return nil, err
		}
		trades = append(trades, t)
	}
	return trades, rows.Err()
}

func (q *Queries) ListSummaries(ctx context.Context, isPaper *bool, limit int) ([]SummaryRow, error) {
	args := []any{limit}
	filter := ""
	if isPaper != nil {
		filter = " WHERE is_paper = $2"
		args = append(args, *isPaper)
	}

	rows, err := q.pool.Query(ctx, `
		SELECT trade_date, trade_count, win_count, loss_count, win_rate, total_pnl, is_paper
		FROM daily_summaries
		`+filter+`
		ORDER BY trade_date DESC
		LIMIT $1
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var summaries []SummaryRow
	for rows.Next() {
		var s SummaryRow
		if err := rows.Scan(&s.TradeDate, &s.TradeCount, &s.WinCount, &s.LossCount, &s.WinRate, &s.TotalPnL, &s.IsPaper); err != nil {
			return nil, err
		}
		summaries = append(summaries, s)
	}
	return summaries, rows.Err()
}

func (q *Queries) GetStats(ctx context.Context, isPaper *bool) ([]StatsRow, error) {
	args := []any{}
	filter := ""
	if isPaper != nil {
		filter = " WHERE is_paper = $1"
		args = append(args, *isPaper)
	}

	rows, err := q.pool.Query(ctx, `
		SELECT
			is_paper,
			COUNT(*) AS total_trades,
			COUNT(*) FILTER (WHERE result = 'WIN') AS win_count,
			COUNT(*) FILTER (WHERE result = 'LOSS') AS loss_count,
			ROUND(
				100.0 * COUNT(*) FILTER (WHERE result = 'WIN') /
				NULLIF(COUNT(*) FILTER (WHERE result IN ('WIN','LOSS')), 0),
			2) AS win_rate,
			COALESCE(SUM(pnl), 0) AS total_pnl
		FROM trades
		`+filter+`
		GROUP BY is_paper
	`, args...)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var stats []StatsRow
	for rows.Next() {
		var s StatsRow
		var winRate *float64
		if err := rows.Scan(&s.IsPaper, &s.TotalTrades, &s.WinCount, &s.LossCount, &winRate, &s.TotalPnL); err != nil {
			return nil, err
		}
		if winRate != nil {
			s.WinRate = *winRate
		}
		stats = append(stats, s)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Attach best day PnL per is_paper group
	bestRows, err := q.pool.Query(ctx, `
		SELECT is_paper, COALESCE(MAX(total_pnl), 0)
		FROM daily_summaries
		`+filter+`
		GROUP BY is_paper
	`, args...)
	if err != nil {
		return nil, err
	}
	defer bestRows.Close()
	bestMap := map[bool]float64{}
	for bestRows.Next() {
		var ip bool
		var best float64
		if err := bestRows.Scan(&ip, &best); err != nil {
			return nil, err
		}
		bestMap[ip] = best
	}
	for i := range stats {
		stats[i].BestDayPnL = bestMap[stats[i].IsPaper]
	}

	return stats, nil
}

func itoa(n int) string {
	return strconv.Itoa(n)
}
