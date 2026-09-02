package grpctransport

import (
	"context"
	"net"
	"net/url"
	"testing"
	"time"

	sessionv1 "github.com/zhangzhe-ctrl/ani-session-gateway/api/gen/session/v1"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/memory"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials/insecure"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
	"google.golang.org/protobuf/proto"
)

func TestHealthAndUnimplementedSession(t *testing.T) {
	lis := bufconn.Listen(1 << 20)
	server := New(nil)
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	response, err := healthv1.NewHealthClient(conn).Check(context.Background(), &healthv1.HealthCheckRequest{})
	if err != nil || response.Status != healthv1.HealthCheckResponse_SERVING {
		t.Fatalf("health: response=%v err=%v", response, err)
	}
	_, err = sessionv1.NewSessionServiceClient(conn).CreateSession(context.Background(), &sessionv1.CreateSessionRequest{})
	if status.Code(err) != codes.Unimplemented {
		t.Fatalf("CreateSession code=%s err=%v", status.Code(err), err)
	}
}

func TestGeneratedClientCreateSessionRoundTripAndReplay(t *testing.T) {
	base, _ := url.Parse("ws://127.0.0.1:30081/api/v1/realtime")
	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	manager, err := session.NewManager(memory.New(), session.ManagerConfig{PublicWSBaseURL: base, TicketKey: key, TicketTTL: time.Minute, SessionMaxDuration: 15 * time.Minute, IdempotencyTTL: 15 * time.Minute, MaxActive: 100, MaxActivePerSubject: 5})
	if err != nil {
		t.Fatal(err)
	}
	lis := bufconn.Listen(1 << 20)
	server := New(NewSessionServer(manager))
	go func() { _ = server.Serve(lis) }()
	t.Cleanup(server.Stop)
	conn, err := grpc.NewClient("passthrough:///bufnet", grpc.WithTransportCredentials(insecure.NewCredentials()), grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return lis.Dial() }))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.Close() })
	client := sessionv1.NewSessionServiceClient(conn)
	request := &sessionv1.CreateSessionRequest{RequestId: "request", IdempotencyKey: "idempotency", Principal: &sessionv1.Principal{TenantId: "tenant", SubjectId: "subject"}, Target: &sessionv1.Target{InstanceId: "instance", WorkloadName: "workload", WorkloadKind: sessionv1.WorkloadKind_WORKLOAD_KIND_CONTAINER}, Mode: &sessionv1.CreateSessionRequest_Exec{Exec: &sessionv1.ExecOptions{Command: []string{"/bin/sh"}, Tty: true, Rows: 24, Cols: 80}}}
	first, err := client.CreateSession(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	second := proto.Clone(request).(*sessionv1.CreateSessionRequest)
	second.RequestId = "retry"
	replayed, err := client.CreateSession(context.Background(), second)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.SessionId != first.SessionId || replayed.ConnectUrl != first.ConnectUrl {
		t.Fatalf("replay changed response: first=%v replay=%v", first, replayed)
	}
	if first.ExpiresAt == nil || first.ConnectUrl == "" {
		t.Fatalf("incomplete response: %v", first)
	}
	request.Target.WorkloadKind = sessionv1.WorkloadKind_WORKLOAD_KIND_VM
	if _, err := client.CreateSession(context.Background(), request); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("invalid request code=%s err=%v", status.Code(err), err)
	}
}
