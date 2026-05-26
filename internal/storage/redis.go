package storage

import (
	"context"
	"encoding/json"
	"fmt"
	"time"

	"github.com/redis/go-redis/v9"

	"futures/internal/config"
	"futures/internal/domain"
)

const (
	keyPositions     = "positions:active"
	keyCooldown      = "state:cooldown"
	keyKillSwitch    = "state:kill_switch"
	keyFundingSnap   = "market:funding_snapshot"
	keyLastWSMessage = "meta:last_ws_message"
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
