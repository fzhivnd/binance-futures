package config

import "fmt"

func (c *Config) Validate() error {
	if c.App.Mode != "paper" && c.App.Mode != "live" {
		return fmt.Errorf("app.mode must be 'paper' or 'live', got: %s", c.App.Mode)
	}
	if c.App.Mode == "live" {
		if c.Binance.APIKey == "" {
			return fmt.Errorf("binance.api_key required for live mode")
		}
		if c.Binance.APISecret == "" {
			return fmt.Errorf("binance.api_secret required for live mode")
		}
	}
	if c.Database.Postgres.User == "" {
		return fmt.Errorf("database.postgres.user is required")
	}
	if c.Database.Postgres.DBName == "" {
		return fmt.Errorf("database.postgres.dbname is required")
	}
	if c.Trading.Leverage < 1 || c.Trading.Leverage > 125 {
		return fmt.Errorf("trading.leverage must be 1–125")
	}
	if c.Trading.PositionSizePct <= 0 || c.Trading.PositionSizePct > 100 {
		return fmt.Errorf("trading.position_size_pct must be 0–100")
	}
	return nil
}
