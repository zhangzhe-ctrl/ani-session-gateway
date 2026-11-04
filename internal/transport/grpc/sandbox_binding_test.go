package grpctransport

import (
	"context"
	sandboxv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/sandbox/terminal/v1"
	sessionv1 "github.com/zhangzhe-ctrl/ani-session-gateway/api/gen/session/v1"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"net/url"
	"testing"
	"time"
)

type sandboxAuthority struct{ calls int }

func (a *sandboxAuthority) ValidateSandbox(context.Context, session.SandboxBinding) error {
	a.calls++
	return nil
}
func TestSandboxRequiresGovernanceAndNoInstance(t *testing.T) {
	base, _ := url.Parse("ws://127.0.0.1/api/v1/realtime")
	v := &sandboxAuthority{}
	m, err := session.NewManager(memory.New(), session.ManagerConfig{SandboxValidator: v, PublicWSBaseURL: base, TicketTTL: time.Minute, SessionMaxDuration: time.Minute, IdempotencyTTL: time.Minute, MaxActive: 2, MaxActivePerSubject: 2})
	if err != nil {
		t.Fatal(err)
	}
	server := NewSessionServer(m)
	b := &sandboxv1.SandboxTerminalBinding{TenantId: "tenant", SandboxId: "sandbox-id", Version: 2, ClusterId: "cluster", Namespace: "tenant-tenant", NamespaceUid: "ns-uid", PodName: "sandbox-sandbox-id", PodUid: "pod-uid", ContainerName: "sandbox", Shell: "/bin/sh"}
	req := &sessionv1.CreateSessionRequest{IdempotencyKey: "key", Principal: &sessionv1.Principal{TenantId: "tenant", SubjectId: "subject"}, Target: &sessionv1.Target{WorkloadName: b.PodName, WorkloadKind: sessionv1.WorkloadKind_WORKLOAD_KIND_SANDBOX, SandboxBinding: b}, Mode: &sessionv1.CreateSessionRequest_Exec{Exec: &sessionv1.ExecOptions{Container: "sandbox", Tty: true, Rows: 24, Cols: 80}}}
	for _, ctx := range []context.Context{context.Background(), governanceContext("spiffe://ani.internal/service/ani-governance", false), governanceContext("spiffe://ani.internal/service/ani-session-gateway", true)} {
		if _, err := server.CreateSession(ctx, req); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("unverified peer accepted: %v", err)
		}
	}
	ctx := governanceContext("spiffe://ani.internal/service/ani-governance", true)
	issued, err := server.CreateSession(ctx, req)
	if err != nil || issued.SessionId == "" || issued.ConnectUrl == "" || v.calls != 1 {
		t.Fatalf("valid sandbox failed: %v", err)
	}
	req.Target.InstanceId = "other"
	if _, err := server.CreateSession(ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("legacy Instance mixed with sandbox: %v", err)
	}
	req.Target.InstanceId = ""
	req.Principal.TenantId = "other"
	if _, err := server.CreateSession(ctx, req); status.Code(err) != codes.InvalidArgument {
		t.Fatalf("tenant mismatch accepted: %v", err)
	}
}
