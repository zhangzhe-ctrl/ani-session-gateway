package store

import (
	"context"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/config"
)

func TestSelectExplicitMemoryIsLocalAndDegraded(t *testing.T) {
	selected, err := Select(context.Background(), config.Config{StoreMode: "memory"})
	if err != nil || selected.Store.Mode() != "memory" || !selected.Degraded {
		t.Fatalf("memory selection: %#v %v", selected, err)
	}
	if err := selected.Store.Ready(context.Background()); err != nil {
		t.Fatalf("explicit memory store is not ready: %v", err)
	}
}

func TestSelectRedisFailureDoesNotFallback(t *testing.T) {
	ctx, cancel := context.WithTimeout(context.Background(), 50*time.Millisecond)
	defer cancel()
	selected, err := Select(ctx, config.Config{StoreMode: "redis", RedisURL: "redis://127.0.0.1:1/0"})
	if err == nil || selected.Store != nil {
		t.Fatalf("redis failure silently selected a store: %#v %v", selected, err)
	}
	if _, err := Select(context.Background(), config.Config{StoreMode: "auto"}); err == nil {
		t.Fatal("obsolete auto mode accepted")
	}
}
