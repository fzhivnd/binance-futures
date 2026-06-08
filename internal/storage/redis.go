package storage

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"futures/internal/config"
	"futures/internal/domain"
)

const (
	keyPositions          = "positions:active"
	keyCooldown           = "state:cooldown"
	keyKillSwitch         = "state:kill_switch"
	keyFundingSnap        = "market:funding_snapshot"
	keyLastWSMessage      = "meta:last_ws_message"
	keyPendingEntryPrefix = "pending_entry:"
	keyPendingProtPrefix  = "pending_prot:"
	keyDailyLossPrefix    = "risk:daily_losses:"
)

type RedisStateCache struct {
	client *redis.Client
}

func NewRedisClient(cfg config.RedisConfig) (*redis.Client, error) {
	client := redis.NewClient(&redis.Options{
		Addr:     cfg.Addr,
		Password: cfg.Password,
		DB:       cfg.DB,
	})
	if err := client.Ping(context.Background()).Err(); err != nil {
		return nil, fmt.Errorf("ping redis: %w", err)
	}
	return client, nil
}

func NewRedisStateCache(client *redis.Client) *RedisStateCache {
	return &RedisStateCache{client: client}
}

func (r *RedisStateCache) GetActivePositions(ctx context.Context) ([]domain.Position, error) {
	vals, err := r.client.HGetAll(ctx, keyPositions).Result()
	if err != nil {
		return nil, err
	}
	var positions []domain.Position
	for _, v := range vals {
		var p domain.Position
		if err := json.Unmarshal([]byte(v), &p); err != nil {
			continue
		}
		positions = append(positions, p)
	}
	return positions, nil
}

func (r *RedisStateCache) GetActivePosition(ctx context.Context, symbol string) (*domain.Position, error) {
	positions, err := r.GetActivePositions(ctx)
	if err != nil {
		return nil, err
	}
	var pos *domain.Position
	for i := range positions {
		if positions[i].Symbol == symbol {
			pos = &positions[i]
			break
		}
	}
	if pos == nil {
		return nil, fmt.Errorf("position not found")
	}
	return pos, nil
}

func (r *RedisStateCache) SetActivePosition(ctx context.Context, pos domain.Position) error {
	data, err := json.Marshal(pos)
	if err != nil {
		return err
	}
	return r.client.HSet(ctx, keyPositions, pos.Symbol, data).Err()
}

func (r *RedisStateCache) RemovePosition(ctx context.Context, symbol string) error {
	return r.client.HDel(ctx, keyPositions, symbol).Err()
}

func (r *RedisStateCache) IsOnCooldown(ctx context.Context) (bool, error) {
	exists, err := r.client.Exists(ctx, keyCooldown).Result()
	if err != nil {
		return false, err
	}
	return exists > 0, nil
}

func (r *RedisStateCache) SetCooldown(ctx context.Context, duration time.Duration) error {
	return r.client.Set(ctx, keyCooldown, "1", duration).Err()
}

func (r *RedisStateCache) GetKillSwitch(ctx context.Context) (bool, error) {
	val, err := r.client.Get(ctx, keyKillSwitch).Result()
	if err == redis.Nil {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return val == "1", nil
}

func (r *RedisStateCache) SetKillSwitch(ctx context.Context, active bool) error {
	val := "0"
	if active {
		val = "1"
	}
	return r.client.Set(ctx, keyKillSwitch, val, 0).Err()
}

func (r *RedisStateCache) AcquireSchedulerLock(ctx context.Context, key string, ttl time.Duration) (bool, error) {
	ok, err := r.client.SetNX(ctx, key, "1", ttl).Result()
	return ok, err
}

func (r *RedisStateCache) SetFundingSnapshot(ctx context.Context, rates map[string]float64) error {
	pipe := r.client.Pipeline()
	pipe.Del(ctx, keyFundingSnap)
	for sym, rate := range rates {
		pipe.HSet(ctx, keyFundingSnap, sym, rate)
	}
	_, err := pipe.Exec(ctx)
	return err
}

func (r *RedisStateCache) GetFundingSnapshot(ctx context.Context) (map[string]float64, error) {
	vals, err := r.client.HGetAll(ctx, keyFundingSnap).Result()
	if err != nil {
		return nil, err
	}
	result := make(map[string]float64, len(vals))
	for k, v := range vals {
		var f float64
		fmt.Sscanf(v, "%f", &f)
		result[k] = f
	}
	return result, nil
}

func (r *RedisStateCache) SetLastWSMessage(ctx context.Context, t time.Time) error {
	return r.client.Set(ctx, keyLastWSMessage, t.UnixMilli(), 0).Err()
}

// — Phase 7: PendingEntry ——————————————————————————————————————————

func (r *RedisStateCache) SetPendingEntry(ctx context.Context, entry domain.PendingEntry) error {
	data, err := json.Marshal(entry)
	if err != nil {
		return err
	}
	key := keyPendingEntryPrefix + entry.TradeId.String()
	ttl := time.Until(entry.ExpiresAt)
	if ttl <= 0 {
		ttl = 60 * time.Second
	}
	return r.client.Set(ctx, key, data, ttl).Err()
}

func (r *RedisStateCache) GetPendingEntry(ctx context.Context, tradeID string) (*domain.PendingEntry, error) {
	val, err := r.client.Get(ctx, keyPendingEntryPrefix+tradeID).Result()
	if errors.Is(err, redis.Nil) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var entry domain.PendingEntry
	if err := json.Unmarshal([]byte(val), &entry); err != nil {
		return nil, err
	}
	return &entry, nil
}

func (r *RedisStateCache) RemovePendingEntry(ctx context.Context, tradeID string) error {
	return r.client.Del(ctx, keyPendingEntryPrefix+tradeID).Err()
}

func (r *RedisStateCache) GetAllPendingEntries(ctx context.Context) ([]domain.PendingEntry, error) {
	keys, err := r.client.Keys(ctx, keyPendingEntryPrefix+"*").Result()
	if err != nil {
		return nil, err
	}
	if len(keys) == 0 {
		return nil, nil
	}
	vals, err := r.client.MGet(ctx, keys...).Result()
	if err != nil {
		return nil, err
	}
	var entries []domain.PendingEntry
	for _, v := range vals {
		if v == nil {
			continue
		}
		var entry domain.PendingEntry
		if err := json.Unmarshal([]byte(v.(string)), &entry); err != nil {
			continue
		}
		entries = append(entries, entry)
	}
	return entries, nil
}

// — Phase 7: PendingProtection ————————————————————————————————————

func (r *RedisStateCache) SetPendingProtection(ctx context.Context, p domain.PendingProtection) error {
	data, err := json.Marshal(p)
	if err != nil {
		return err
	}
	return r.client.Set(ctx, keyPendingProtPrefix+p.Symbol, data, 0).Err()
}

func (r *RedisStateCache) GetPendingProtection(ctx context.Context, symbol string) (*domain.PendingProtection, error) {
	val, err := r.client.Get(ctx, keyPendingProtPrefix+symbol).Result()
	if err == redis.Nil {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	var p domain.PendingProtection
	if err := json.Unmarshal([]byte(val), &p); err != nil {
		return nil, err
	}
	return &p, nil
}

func (r *RedisStateCache) RemovePendingProtection(ctx context.Context, symbol string) error {
	return r.client.Del(ctx, keyPendingProtPrefix+symbol).Err()
}

// — Daily loss counter ————————————————————————————————————————————

func dailyLossKey() string {
	return keyDailyLossPrefix + time.Now().UTC().Format("2006-01-02")
}

func (r *RedisStateCache) GetDailyLossCount(ctx context.Context) (int, error) {
	val, err := r.client.Get(ctx, dailyLossKey()).Result()
	if err == redis.Nil {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	var count int
	fmt.Sscanf(val, "%d", &count)
	return count, nil
}

func (r *RedisStateCache) IncrDailyLossCount(ctx context.Context) error {
	key := dailyLossKey()
	pipe := r.client.Pipeline()
	pipe.Incr(ctx, key)
	pipe.Expire(ctx, key, 26*time.Hour)
	_, err := pipe.Exec(ctx)
	return err
}
