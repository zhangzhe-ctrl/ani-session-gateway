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
	"github.com/redis/go-redis/v9"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/storetest"
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

type instanceAuthority struct{}

func (instanceAuthority) Validate(context.Context, string, string, session.InstanceBinding) error {
	return nil
}
func TestManagedInstanceRedisBindingSurvivesManagerRestart(t *testing.T) {
	server := miniredis.RunT(t)
	client := redis.NewClient(&redis.Options{Addr: server.Addr()})
	defer client.Close()
	base, _ := url.Parse("ws://127.0.0.1:30081/api/v1/realtime")
	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	config := session.ManagerConfig{InstanceValidator: instanceAuthority{}, PublicWSBaseURL: base, TicketKey: key, TicketTTL: time.Minute, SessionMaxDuration: 15 * time.Minute, IdempotencyTTL: 15 * time.Minute, MaxActive: 10, MaxActivePerSubject: 5}
	manager, e := session.NewManager(New(client), config)
	if e != nil {
		t.Fatal(e)
	}
	request := session.Request{IdempotencyKey: "managed-instance", TenantID: "tenant", SubjectID: "subject", InstanceID: "instance", WorkloadName: "notebook", WorkloadKind: session.WorkloadGPUContainer, Mode: session.ModeExec, Container: "workspace", Command: []string{"/bin/sh"}, Rows: 24, Cols: 80, InstanceBinding: session.InstanceBinding{Enabled: true, Version: 3, ClusterID: "cluster", Namespace: "saved-tenant", NotebookName: "notebook", NotebookUID: "notebook-original", PodName: "notebook-0", PodUID: "pod-original", ContainerName: "workspace", Shell: "/bin/sh"}}
	issued, e := manager.Issue(context.Background(), request)
	if e != nil {
		t.Fatal(e)
	}
	manager, e = session.NewManager(New(client), config)
	if e != nil {
		t.Fatal(e)
	}
	access, e := manager.Claim(context.Background(), issued.Session.ID, issued.Ticket)
	if e != nil || access.Target.InstanceBinding != request.InstanceBinding {
		t.Fatal("persisted target binding lost on restart", e)
	}
}
