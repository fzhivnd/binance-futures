package config

import "testing"

func validConfig() *Config {
	return &Config{
		App: AppConfig{Mode: "paper"},
		Database: DatabaseConfig{
			Postgres: PostgresConfig{User: "postgres", DBName: "futures"},
		},
		Trading: TradingConfig{Leverage: 20, PositionSizePct: 3.0},
	}
}

func TestValidate_ValidPaperConfig(t *testing.T) {
	cfg := validConfig()
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error, got: %v", err)
	}
}

func TestValidate_InvalidMode(t *testing.T) {
	cfg := validConfig()
	cfg.App.Mode = "test"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for invalid mode")
	}
}

func TestValidate_LiveModeRequiresKeys(t *testing.T) {
	cfg := validConfig()
	cfg.App.Mode = "live"

	if err := cfg.Validate(); err == nil {
		t.Error("expected error when api_key missing in live mode")
	}

	cfg.Binance.APIKey = "key"
	if err := cfg.Validate(); err == nil {
		t.Error("expected error when api_secret missing in live mode")
	}

	cfg.Binance.APISecret = "secret"
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected no error with both keys set, got: %v", err)
	}
}

func TestValidate_MissingDBUser(t *testing.T) {
	cfg := validConfig()
	cfg.Database.Postgres.User = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing postgres user")
	}
}

func TestValidate_MissingDBName(t *testing.T) {
	cfg := validConfig()
	cfg.Database.Postgres.DBName = ""
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for missing dbname")
	}
}

func TestValidate_LeverageBounds(t *testing.T) {
	cfg := validConfig()

	cfg.Trading.Leverage = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for leverage=0")
	}

	cfg.Trading.Leverage = 126
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for leverage=126")
	}

	cfg.Trading.Leverage = 1
	if err := cfg.Validate(); err != nil {
		t.Errorf("leverage=1 should be valid, got: %v", err)
	}

	cfg.Trading.Leverage = 125
	if err := cfg.Validate(); err != nil {
		t.Errorf("leverage=125 should be valid, got: %v", err)
	}
}

func TestValidate_PositionSizePctBounds(t *testing.T) {
	cfg := validConfig()

	cfg.Trading.PositionSizePct = 0
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for position_size_pct=0")
	}

	cfg.Trading.PositionSizePct = 101
	if err := cfg.Validate(); err == nil {
		t.Error("expected error for position_size_pct=101")
	}

	cfg.Trading.PositionSizePct = 100
	if err := cfg.Validate(); err != nil {
		t.Errorf("position_size_pct=100 should be valid, got: %v", err)
	}
}
