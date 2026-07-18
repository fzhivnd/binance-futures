package notify

import (
	"fmt"
	"strings"

	"futures/internal/domain"
)

func pnlSign(v float64) string {
	if v >= 0 {
		return "+"
	}
	return ""
}

func escapeMarkdownV2(s string) string {
	replacer := strings.NewReplacer(
		"_", "\\_", "*", "\\*", "[", "\\[", "]", "\\]",
		"(", "\\(", ")", "\\)", "~", "\\~", "`", "\\`",
		">", "\\>", "#", "\\#", "+", "\\+", "-", "\\-",
		"=", "\\=", "|", "\\|", "{", "\\{", "}", "\\}",
		".", "\\.", "!", "\\!",
	)
	return replacer.Replace(s)
}

func FormatTradeOpened(e TradeOpenedEvent) string {
	mode := "LIVE"
	if e.IsPaper {
		mode = "PAPER"
	}
	openedAt := e.OpenedAt.UTC().Format("2006-01-02 15:04:05 UTC")

	return fmt.Sprintf(
		"*SHORT OPENED* \\[%s\\]\n\n"+
			"Symbol: `%s`\n"+
			"Entry: `$%s` \\| Margin: `$%.2f`\n"+
			"Leverage: `%dx`\n"+
			"SL: `$%s` \\| TP: `$%s`\n"+
			"Mode: `%s` \\| Confidence: `%d`\n"+
			"Entry Mode: `%s`\n"+
			"Funding Rate: `%.4f%%`\n"+
			"Opened: `%s`",
		escapeMarkdownV2(e.Symbol),
		escapeMarkdownV2(e.Symbol),
		escapeMarkdownV2(fmt.Sprintf("%.8g", e.EntryPrice)),
		e.Quantity*e.EntryPrice/float64(e.Leverage),
		e.Leverage,
		escapeMarkdownV2(fmt.Sprintf("%.8g", e.StopLoss)),
		escapeMarkdownV2(fmt.Sprintf("%.8g", e.TakeProfit)),
		mode,
		e.Confidence,
		escapeMarkdownV2(e.EntryMode),
		e.FundingRate*100,
		escapeMarkdownV2(openedAt),
	)
}

func FormatTradeClosed(e TradeClosedEvent) string {
	mode := "LIVE"
	if e.IsPaper {
		mode = "PAPER"
	}

	emoji := "🔴"
	switch e.Result {
	case "WIN", "PARTIAL_WIN":
		emoji = "🟢"
	case "BREAKEVEN":
		emoji = "⚪"
	case "FORCE_SL":
		emoji = "🟡"
	}

	pnlSign := "+"
	if e.PnL < 0 {
		pnlSign = ""
	}

	return fmt.Sprintf(
		"%s *TRADE CLOSED* \\[%s\\]\n\n"+
			"Symbol: `%s`\n"+
			"Result: `%s` \\(%s\\)\n"+
			"Entry: `$%s` → Exit: `$%s`\n"+
			"PnL: `%s%.4f USDT` \\(`%s%.1f%%`\\)\n"+
			"Hold: `%s`\n"+
			"Mode: `%s`",
		emoji,
		escapeMarkdownV2(e.Symbol),
		escapeMarkdownV2(e.Symbol),
		escapeMarkdownV2(e.Result),
		escapeMarkdownV2(e.CloseReason),
		escapeMarkdownV2(fmt.Sprintf("%.8g", e.EntryPrice)),
		escapeMarkdownV2(fmt.Sprintf("%.8g", e.ExitPrice)),
		pnlSign, e.PnL,
		pnlSign, e.PnLPct,
		escapeMarkdownV2(e.HoldDuration),
		mode,
	)
}

func FormatDailySummary(s *domain.DailySummary) string {
	mode := "LIVE"
	if s.IsPaper {
		mode = "PAPER"
	}

	pnlSign := "+"
	if s.TotalPnL < 0 {
		pnlSign = ""
	}

	status := "📊"
	if s.TradeCount == 0 {
		status = "😴"
	} else if s.TotalPnL > 0 {
		status = "📈"
	} else if s.TotalPnL < 0 {
		status = "📉"
	}

	return fmt.Sprintf(
		"%s *DAILY SUMMARY* \\[%s\\]\n"+
			"Date: `%s`\n\n"+
			"Trades: `%d`\n"+
			"Wins: `%d` \\| Losses: `%d` \\| Breakeven: `%d`\n"+
			"Win Rate: `%.1f%%`\n"+
			"Total PnL: `%s%.4f USDT`\n"+
			"Mode: `%s`",
		status,
		escapeMarkdownV2(s.TradeDate.Format("2006-01-02")),
		escapeMarkdownV2(s.TradeDate.Format("2006-01-02")),
		s.TradeCount,
		s.WinCount, s.LossCount, s.TradeCount-s.WinCount-s.LossCount,
		s.WinRate,
		pnlSign, s.TotalPnL,
		mode,
	)
}

func FormatRiskEvent(e RiskEvent) string {
	emoji := "⚠️"
	switch e.Type {
	case "kill_switch":
		emoji = "🚨"
	case "daily_loss_limit":
		emoji = "🛑"
	case "cooldown":
		emoji = "⏸️"
	}

	return fmt.Sprintf(
		"%s *RISK ALERT*\n\n"+
			"Type: `%s`\n"+
			"Detail: %s",
		emoji,
		escapeMarkdownV2(e.Type),
		escapeMarkdownV2(e.Message),
	)
}

func FormatFundingEvent(e FundingEvent) string {
	return fmt.Sprintf(
		"*FUNDING SETTLEMENT*\n\n"+
			"Symbol: `%s`\n"+
			"Funding Rate: `%.4f%%`\n"+
			"Fee Paid: `%.4f USDT`",
		escapeMarkdownV2(e.Symbol),
		e.FundingRate, e.FeePaid)
}

func FormatTp1Event(e Tp1Event) string {
	closedAt := e.ClosedAt.UTC().Format("2006-01-02 15:04:05 UTC")
	return fmt.Sprintf(
		"🟢 *TP1 HIT* \\[%s\\] \n\n"+
			"Symbol: `%s`\n"+
			"Price: `$%s`\n"+
			"PnL: `%s%s%%` \\(`%s%s%% ROI`\\)\n"+
			"Secured: `%s%s USDT`\n"+
			"Time: `%s`\n",
		escapeMarkdownV2(e.Symbol),
		escapeMarkdownV2(e.Symbol),
		escapeMarkdownV2(fmt.Sprintf("%.8g", e.AvgPrice)),
		escapeMarkdownV2(pnlSign(e.PnLPct)), escapeMarkdownV2(fmt.Sprintf("%.2f", e.PnLPct)),
		escapeMarkdownV2(pnlSign(e.PnLROI)), escapeMarkdownV2(fmt.Sprintf("%.1f", e.PnLROI)),
		escapeMarkdownV2(pnlSign(e.SecuredUSDT)), escapeMarkdownV2(fmt.Sprintf("%.2f", e.SecuredUSDT)),
		escapeMarkdownV2(closedAt))
}
