package store

import (
	"context"
	"fmt"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/config"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/memory"
	redisstore "github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/redis"
	"github.com/redis/go-redis/v9"
)

type Selection struct {
	Store    session.Store
	Degraded bool
	Shutdown func() error
}

func Select(ctx context.Context, cfg config.Config) (Selection, error) {
	if cfg.StoreMode == "memory" {
		return Selection{Store: memory.New(), Degraded: true, Shutdown: func() error { return nil }}, nil
	}
	if cfg.StoreMode != "redis" {
		return Selection{}, fmt.Errorf("STORE_MODE must be redis or memory")
	}
	if cfg.RedisURL == "" {
		return Selection{}, fmt.Errorf("redis mode requires REDIS_URL")
	}
	options, err := redis.ParseURL(cfg.RedisURL)
	if err != nil {
		return Selection{}, fmt.Errorf("parse REDIS_URL: %w", err)
	}
	client := redis.NewClient(options)
	selected := redisstore.New(client)
	if err := selected.Ready(ctx); err != nil {
		_ = client.Close()
		return Selection{}, err
	}
	return Selection{Store: selected, Shutdown: client.Close}, nil
}
