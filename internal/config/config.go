package config

import (
	"fmt"
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	App            AppConfig           `yaml:"app"`
	Binance        BinanceConfig       `yaml:"binance"`
	Trading        TradingConfig       `yaml:"trading"`
	Funding        FundingConfig       `yaml:"funding"`
	Execution      ExecutionConfig     `yaml:"execution"`
	Scheduler      SchedulerConfig     `yaml:"scheduler"`
	WebSocket      WebSocketConfig     `yaml:"websocket"`
	Database       DatabaseConfig      `yaml:"database"`
	Scoring        ScoringConfig       `yaml:"scoring"`
	Risk           RiskConfig          `yaml:"risk"`
	LLM            LLMConfig           `yaml:"llm"`
	Memory         MemoryConfig        `yaml:"memory"`
	Telegram       TelegramConfig      `yaml:"telegram"`
	Summary        SummaryConfig       `yaml:"summary"`
	AfterExecution AfterExecConfig     `yaml:"after_execution"`
	PreSettlement  PreSettlementConfig `yaml:"pre_settlement"`
}

// AfterExecConfig controls the Phase 8 AfterTrigger and bid-depth sizing.
type AfterExecConfig struct {
	Enabled                  bool    `yaml:"enabled"`
	SubscribeBeforeSeconds   int     `yaml:"subscribe_before_seconds"`   // subscribe @bookTicker at T-Xs
	MinBidDepthMultiplier    float64 `yaml:"min_bid_depth_multiplier"`   // skip if depth < multiplier * orderSize
	FullSizeDepthMultiplier  float64 `yaml:"full_size_depth_multiplier"` // full size if depth >= multiplier * orderSize
	ReducedSizePct           float64 `yaml:"reduced_size_pct"`           // position size pct when book is thin
	ClockSyncIntervalSeconds int     `yaml:"clock_sync_interval_seconds"`
	FallbackToScheduler      bool    `yaml:"fallback_to_scheduler"`
}

// PreSettlementConfig controls the T-2m pre-settlement check for FRONTRUN/LASTMINUTE positions.
type PreSettlementConfig struct {
	Enabled                 bool    `yaml:"enabled"`
	CheckBeforeMinutes      int     `yaml:"check_before_minutes"`      // fire check at T-Xm
	EmergencyCloseThreshold float64 `yaml:"emergency_close_threshold"` // close if loss > X * |funding_rate|
	WidenTPOnMiss           bool    `yaml:"widen_tp_on_miss"`          // widen TP1 if not filled by T-2m
}

type TelegramConfig struct {
	Enabled     bool   `yaml:"enabled"`
	BotToken    string `yaml:"bot_token"`
	ChatID      string `yaml:"chat_id"`
	TimeoutSecs int    `yaml:"timeout_secs"`
}

type SummaryConfig struct {
	Enabled bool `yaml:"enabled"`
}

type AppConfig struct {
	Mode         string  `yaml:"mode"`
	LogLevel     string  `yaml:"log_level"`
	PaperBalance float64 `yaml:"paper_balance"`
	PprofPort    int     `yaml:"pprof_port"` // 0 = disabled
}

type BinanceConfig struct {
	APIKey    string `yaml:"api_key"`
	APISecret string `yaml:"api_secret"`
	BaseURL   string `yaml:"base_url"`
	WsURL     string `yaml:"ws_url"`
	Testnet   bool   `yaml:"testnet"`
}

type TradingConfig struct {
	MaxPositions    int     `yaml:"max_positions"`
	Leverage        int     `yaml:"leverage"`
	PositionSizePct float64 `yaml:"position_size_pct"`
	CooldownMinutes int     `yaml:"cooldown_minutes"`
}

type FundingConfig struct {
	MinRate       float64 `yaml:"min_rate"`
	MaxRate       float64 `yaml:"max_rate"`
	TopCandidates int     `yaml:"top_candidates"`
}

type ExecutionConfig struct {
	SlPct                  float64 `yaml:"sl_pct"`
	TpPct                  float64 `yaml:"tp_pct"`
	TrailingActivationPct  float64 `yaml:"trailing_activation_pct"`
	BreakevenActivationPct float64 `yaml:"breakeven_activation_pct"`
	SlippageBps            int     `yaml:"slippage_bps"`

	// Phase 5: Split TP + trailing stop
	TrailingCallbackRate float64 `yaml:"trailing_callback_rate"` // e.g. 0.5 = 0.5%
	TP1SizePct           float64 `yaml:"tp1_size_pct"`           // e.g. 50 = 50% of position

	// Phase 5: Force stop-loss
	ForceSLEnabled         bool    `yaml:"force_sl_enabled"`
	ForceSLStartMin        int     `yaml:"force_sl_start_min"`         // start checking after N minutes
	ForceSLPnlGatePct      float64 `yaml:"force_sl_pnl_gate_pct"`      // skip if PnL above this (e.g. -0.5)
	ForceSLEscalatePnlPct  float64 `yaml:"force_sl_escalate_pnl_pct"`  // escalate below this (e.g. -2.0)
	ForceSLSlowIntervalSec int     `yaml:"force_sl_slow_interval_sec"` // interval when mild loss (300s)
	ForceSLFastIntervalSec int     `yaml:"force_sl_fast_interval_sec"` // interval when severe loss (60s)
	ForceSLTimeoutSec      int     `yaml:"force_sl_timeout_sec"`       // LLM call timeout (15s)
}

type SchedulerConfig struct {
	ScanIntervalEarly  string `yaml:"scan_interval_early"`
	ScanIntervalLate   string `yaml:"scan_interval_late"`
	ScanInterval       string `yaml:"scan_interval"` // Phase 3: uniform interval (overrides early/late when set)
	WindowStartMinutes int    `yaml:"window_start_minutes"`
}

type LLMConfig struct {
	Enabled                  bool   `yaml:"enabled"`
	APIKey                   string `yaml:"api_key"`
	Model                    string `yaml:"model"`
	TimeoutSecs              int    `yaml:"timeout_secs"`
	MaxRetries               int    `yaml:"max_retries"`
	MaxRPM                   int    `yaml:"max_rpm"`
	TopCandidates            int    `yaml:"top_candidates"`
	MinConfidence            int    `yaml:"min_confidence"`
	FrontrunExecIntervalSecs int    `yaml:"frontrun_exec_interval_secs"` // how often to execute best FRONTRUN intent (default 300 = 5m)
	CallCooldownSecs         int    `yaml:"call_cooldown_secs"`          // min gap between LLM calls with similar inputs (default 300 = 5m)
}

type WebSocketConfig struct {
	PingInterval         string `yaml:"ping_interval"`
	StaleTimeout         string `yaml:"stale_timeout"`
	MaxReconnectFailures int    `yaml:"max_reconnect_failures"`
	ReconnectBaseBackoff string `yaml:"reconnect_base_backoff"`
	ReconnectMaxBackoff  string `yaml:"reconnect_max_backoff"`
}

type DatabaseConfig struct {
	Postgres PostgresConfig `yaml:"postgres"`
	Redis    RedisConfig    `yaml:"redis"`
}

type ScoringConfig struct {
	MinScore float64 `yaml:"min_score"`
}

type RiskConfig struct {
	MaxDailyLosses    int     `yaml:"max_daily_losses"`
	MaxDrawdownPct    float64 `yaml:"max_drawdown_pct"`
	MaxATRRatio       float64 `yaml:"max_atr_ratio"`
	BTCBreakoutReject bool    `yaml:"btc_breakout_reject"`
}

type MemoryConfig struct {
	Enabled             bool    `yaml:"enabled"`
	EmbeddingModel      string  `yaml:"embedding_model"`
	EmbeddingDimensions int     `yaml:"embedding_dimensions"`
	TopSimilar          int     `yaml:"top_similar"`
	MinSimilarity       float64 `yaml:"min_similarity"`
	EmbedSkips          bool    `yaml:"embed_skips"`
	SkipValidationDelay string  `yaml:"skip_validation_delay"`
	MaxMemoryAge        string  `yaml:"max_memory_age"`
	SummarizerModel     string  `yaml:"summarizer_model"`
}

type PostgresConfig struct {
	Host     string `yaml:"host"`
	Port     int    `yaml:"port"`
	User     string `yaml:"user"`
	Password string `yaml:"password"`
	DBName   string `yaml:"dbname"`
	SSLMode  string `yaml:"sslmode"`
	MaxConns int    `yaml:"max_conns"`
}

type RedisConfig struct {
	Addr     string `yaml:"addr"`
	Password string `yaml:"password"`
	DB       int    `yaml:"db"`
}

func Load(path string) (*Config, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("read config: %w", err)
	}

	expanded := os.ExpandEnv(string(data))

	var cfg Config
	if err := yaml.Unmarshal([]byte(expanded), &cfg); err != nil {
		return nil, fmt.Errorf("parse config: %w", err)
	}

	setDefaults(&cfg)
	return &cfg, nil
}

func setDefaults(cfg *Config) {
	if cfg.App.Mode == "" {
		cfg.App.Mode = "paper"
	}
	if cfg.App.LogLevel == "" {
		cfg.App.LogLevel = "info"
	}
	if cfg.App.PaperBalance == 0 {
		cfg.App.PaperBalance = 1000.0
	}
	if cfg.Binance.BaseURL == "" {
		cfg.Binance.BaseURL = "https://fapi.binance.com"
	}
	if cfg.Binance.WsURL == "" {
		cfg.Binance.WsURL = "wss://fstream.binance.com"
	}
	if cfg.Trading.MaxPositions == 0 {
		cfg.Trading.MaxPositions = 2
	}
	if cfg.Trading.Leverage == 0 {
		cfg.Trading.Leverage = 20
	}
	if cfg.Trading.PositionSizePct == 0 {
		cfg.Trading.PositionSizePct = 3.0
	}
	if cfg.Trading.CooldownMinutes == 0 {
		cfg.Trading.CooldownMinutes = 15
	}
	if cfg.Funding.MinRate == 0 {
		cfg.Funding.MinRate = -0.02
	}
	if cfg.Funding.MaxRate == 0 {
		cfg.Funding.MaxRate = -0.002
	}
	if cfg.Funding.TopCandidates == 0 {
		cfg.Funding.TopCandidates = 10
	}
	if cfg.Execution.SlPct == 0 {
		cfg.Execution.SlPct = 5.0
	}
	if cfg.Execution.TpPct == 0 {
		cfg.Execution.TpPct = 2.0
	}
	if cfg.Execution.BreakevenActivationPct == 0 {
		cfg.Execution.BreakevenActivationPct = 1.0
	}
	if cfg.Execution.SlippageBps == 0 {
		cfg.Execution.SlippageBps = 1
	}
	if cfg.Scheduler.ScanIntervalEarly == "" {
		cfg.Scheduler.ScanIntervalEarly = "5m"
	}
	if cfg.Scheduler.ScanIntervalLate == "" {
		cfg.Scheduler.ScanIntervalLate = "1m"
	}
	if cfg.Scheduler.WindowStartMinutes == 0 {
		cfg.Scheduler.WindowStartMinutes = 30
	}
	if cfg.WebSocket.PingInterval == "" {
		cfg.WebSocket.PingInterval = "2m"
	}
	if cfg.WebSocket.StaleTimeout == "" {
		cfg.WebSocket.StaleTimeout = "30s"
	}
	if cfg.WebSocket.MaxReconnectFailures == 0 {
		cfg.WebSocket.MaxReconnectFailures = 5
	}
	if cfg.WebSocket.ReconnectBaseBackoff == "" {
		cfg.WebSocket.ReconnectBaseBackoff = "1s"
	}
	if cfg.WebSocket.ReconnectMaxBackoff == "" {
		cfg.WebSocket.ReconnectMaxBackoff = "30s"
	}
	if cfg.Database.Postgres.Host == "" {
		cfg.Database.Postgres.Host = "localhost"
	}
	if cfg.Database.Postgres.Port == 0 {
		cfg.Database.Postgres.Port = 5432
	}
	if cfg.Database.Postgres.SSLMode == "" {
		cfg.Database.Postgres.SSLMode = "disable"
	}
	if cfg.Database.Postgres.MaxConns == 0 {
		cfg.Database.Postgres.MaxConns = 10
	}
	if cfg.Database.Redis.Addr == "" {
		cfg.Database.Redis.Addr = "localhost:6379"
	}

	if cfg.Scoring.MinScore == 0 {
		cfg.Scoring.MinScore = 60.0
	}

	if cfg.Risk.MaxDailyLosses == 0 {
		cfg.Risk.MaxDailyLosses = 2
	}
	if cfg.Risk.MaxDrawdownPct == 0 {
		cfg.Risk.MaxDrawdownPct = 10.0
	}
	if cfg.Risk.MaxATRRatio == 0 {
		cfg.Risk.MaxATRRatio = 6.0
	}
	if !cfg.Risk.BTCBreakoutReject {
		cfg.Risk.BTCBreakoutReject = true
	}
	// Phase 3: LLM defaults
	if cfg.LLM.Model == "" {
		cfg.LLM.Model = "gpt-4.1-mini"
	}

	if cfg.LLM.TimeoutSecs == 0 {
		cfg.LLM.TimeoutSecs = 20
	}
	if cfg.LLM.MaxRetries == 0 {
		cfg.LLM.MaxRetries = 1
	}
	if cfg.LLM.MaxRPM == 0 {
		cfg.LLM.MaxRPM = 30
	}
	if cfg.LLM.TopCandidates == 0 {
		cfg.LLM.TopCandidates = 5
	}
	if cfg.LLM.MinConfidence == 0 {
		cfg.LLM.MinConfidence = 60
	}
	if cfg.LLM.FrontrunExecIntervalSecs == 0 {
		cfg.LLM.FrontrunExecIntervalSecs = 300 // 5 minutes
	}
	if cfg.LLM.CallCooldownSecs == 0 {
		cfg.LLM.CallCooldownSecs = 300 // 5 minutes
	}

	// Phase 5: Split TP + force-SL defaults
	if cfg.Execution.TrailingCallbackRate == 0 {
		cfg.Execution.TrailingCallbackRate = 0.5
	}
	if cfg.Execution.TP1SizePct == 0 {
		cfg.Execution.TP1SizePct = 50
	}
	if cfg.Execution.ForceSLStartMin == 0 {
		cfg.Execution.ForceSLStartMin = 5
	}
	if cfg.Execution.ForceSLPnlGatePct == 0 {
		cfg.Execution.ForceSLPnlGatePct = -0.5
	}
	if cfg.Execution.ForceSLEscalatePnlPct == 0 {
		cfg.Execution.ForceSLEscalatePnlPct = -2.0
	}
	if cfg.Execution.ForceSLSlowIntervalSec == 0 {
		cfg.Execution.ForceSLSlowIntervalSec = 300
	}
	if cfg.Execution.ForceSLFastIntervalSec == 0 {
		cfg.Execution.ForceSLFastIntervalSec = 60
	}
	if cfg.Execution.ForceSLTimeoutSec == 0 {
		cfg.Execution.ForceSLTimeoutSec = 20
	}

	// Phase 8: AfterExecution defaults
	if cfg.AfterExecution.SubscribeBeforeSeconds == 0 {
		cfg.AfterExecution.SubscribeBeforeSeconds = 5
	}
	if cfg.AfterExecution.MinBidDepthMultiplier == 0 {
		cfg.AfterExecution.MinBidDepthMultiplier = 1.0
	}
	if cfg.AfterExecution.FullSizeDepthMultiplier == 0 {
		cfg.AfterExecution.FullSizeDepthMultiplier = 3.0
	}
	if cfg.AfterExecution.ReducedSizePct == 0 {
		cfg.AfterExecution.ReducedSizePct = 50.0
	}
	if cfg.AfterExecution.ClockSyncIntervalSeconds == 0 {
		cfg.AfterExecution.ClockSyncIntervalSeconds = 300
	}
	if !cfg.AfterExecution.FallbackToScheduler {
		cfg.AfterExecution.FallbackToScheduler = true
	}

	// Phase 8: PreSettlement defaults
	if cfg.PreSettlement.CheckBeforeMinutes == 0 {
		cfg.PreSettlement.CheckBeforeMinutes = 2
	}
	if cfg.PreSettlement.EmergencyCloseThreshold == 0 {
		cfg.PreSettlement.EmergencyCloseThreshold = 0.75
	}
	if !cfg.PreSettlement.WidenTPOnMiss {
		cfg.PreSettlement.WidenTPOnMiss = true
	}

	// Phase 6: Telegram defaults
	if cfg.Telegram.TimeoutSecs == 0 {
		cfg.Telegram.TimeoutSecs = 10
	}
	if !cfg.Summary.Enabled {
		cfg.Summary.Enabled = true
	}

	// Phase 4: Memory defaults
	if cfg.Memory.EmbeddingModel == "" {
		cfg.Memory.EmbeddingModel = "text-embedding-3-small"
	}
	if cfg.Memory.EmbeddingDimensions == 0 {
		cfg.Memory.EmbeddingDimensions = 1536
	}
	if cfg.Memory.TopSimilar == 0 {
		cfg.Memory.TopSimilar = 5
	}
	if cfg.Memory.MinSimilarity == 0 {
		cfg.Memory.MinSimilarity = 0.75
	}
	if cfg.Memory.SkipValidationDelay == "" {
		cfg.Memory.SkipValidationDelay = "2h"
	}
	if cfg.Memory.MaxMemoryAge == "" {
		cfg.Memory.MaxMemoryAge = "90d"
	}
	if cfg.Memory.SummarizerModel == "" {
		cfg.Memory.SummarizerModel = "gpt-4.1-mini"
	}
}

func (c *WebSocketConfig) GetStaleTimeout() time.Duration {
	d, err := time.ParseDuration(c.StaleTimeout)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

func (c *WebSocketConfig) GetPingInterval() time.Duration {
	d, err := time.ParseDuration(c.PingInterval)
	if err != nil {
		return 2 * time.Minute
	}
	return d
}

func (c *WebSocketConfig) GetReconnectBaseBackoff() time.Duration {
	d, err := time.ParseDuration(c.ReconnectBaseBackoff)
	if err != nil {
		return time.Second
	}
	return d
}

func (c *WebSocketConfig) GetReconnectMaxBackoff() time.Duration {
	d, err := time.ParseDuration(c.ReconnectMaxBackoff)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

func (c *SchedulerConfig) GetScanIntervalEarly() time.Duration {
	if c.ScanInterval != "" {
		if d, err := time.ParseDuration(c.ScanInterval); err == nil {
			return d
		}
	}
	d, err := time.ParseDuration(c.ScanIntervalEarly)
	if err != nil {
		return 5 * time.Minute
	}
	return d
}

func (c *SchedulerConfig) GetScanIntervalLate() time.Duration {
	if c.ScanInterval != "" {
		if d, err := time.ParseDuration(c.ScanInterval); err == nil {
			return d
		}
	}
	d, err := time.ParseDuration(c.ScanIntervalLate)
	if err != nil {
		return time.Minute
	}
	return d
}

func (c *SchedulerConfig) GetScanInterval() time.Duration {
	if c.ScanInterval != "" {
		if d, err := time.ParseDuration(c.ScanInterval); err == nil {
			return d
		}
	}
	return time.Minute
}

// GetMaxMemoryAge parses the max_memory_age string (e.g. "90d", "30d", "24h").
// Falls back to 90 days if parsing fails.
func (c *MemoryConfig) GetMaxMemoryAge() time.Duration {
	s := c.MaxMemoryAge
	if len(s) > 1 && s[len(s)-1] == 'd' {
		days := 0
		if _, err := fmt.Sscanf(s[:len(s)-1], "%d", &days); err == nil {
			return time.Duration(days) * 24 * time.Hour
		}
	}
	if d, err := time.ParseDuration(s); err == nil {
		return d
	}
	return 90 * 24 * time.Hour
}

// GetSkipValidationDelay parses the skip_validation_delay string.
func (c *MemoryConfig) GetSkipValidationDelay() time.Duration {
	if d, err := time.ParseDuration(c.SkipValidationDelay); err == nil {
		return d
	}
	return 2 * time.Hour
}
