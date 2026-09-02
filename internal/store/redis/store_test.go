package redisstore

import (
	"context"
	"errors"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/alicebob/miniredis/v2"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/storetest"
	"github.com/redis/go-redis/v9"
)

func TestContractAgainstProtocolCompatibleLocalRedis(t *testing.T) {
	server := miniredis.RunT(t)
	storetest.Run(t, func(t *testing.T) session.Store {
		server.FlushAll()
		client := redis.NewClient(&redis.Options{Addr: server.Addr()})
		t.Cleanup(func() { _ = client.Close() })
		return New(client)
	})
}

func TestContractAgainstRealRedis(t *testing.T) {
	address := os.Getenv("REDIS_TEST_ADDR")
	if address == "" {
		t.Skip("not_verified: REDIS_TEST_ADDR is not configured")
	}
	storetest.Run(t, func(t *testing.T) session.Store {
		client := redis.NewClient(&redis.Options{Addr: address, DB: 15})
		if err := client.FlushDB(t.Context()).Err(); err != nil {
			t.Fatalf("prepare real Redis: %v", err)
		}
		t.Cleanup(func() { _ = client.FlushDB(t.Context()).Err(); _ = client.Close() })
		return New(client)
	})
}

func TestRedisContainsNoPlaintextTicketAndDoesNotFallbackAfterFailure(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	store := New(client)
	base, _ := url.Parse("ws://127.0.0.1:30081/api/v1/realtime")
	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	manager, err := session.NewManager(store, session.ManagerConfig{PublicWSBaseURL: base, TicketKey: key, TicketTTL: time.Minute, SessionMaxDuration: 15 * time.Minute, IdempotencyTTL: 15 * time.Minute, MaxActive: 10, MaxActivePerSubject: 5})
	if err != nil {
		t.Fatal(err)
	}
	request := session.Request{IdempotencyKey: "idem-sensitive", TenantID: "tenant", SubjectID: "subject", InstanceID: "instance", WorkloadName: "workload", WorkloadKind: session.WorkloadContainer, Mode: session.ModeExec, Command: []string{"/bin/sh"}, Rows: 24, Cols: 80}
	issued, err := manager.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(server.Dump(), issued.Ticket) {
		t.Fatal("Redis contains plaintext ticket")
	}
	server.Close()
	request.IdempotencyKey = "after-redis-failure"
	if _, err := manager.Issue(context.Background(), request); !errors.Is(err, session.ErrUnavailable) {
		t.Fatalf("runtime Redis failure error=%v", err)
	}
	_ = client.Close()
}
