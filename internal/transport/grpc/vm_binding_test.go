package grpctransport

import (
	"context"
	"crypto/x509"
	"net/url"
	"testing"
	"time"

	consolev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/console/v1"
	sessionv1 "github.com/zhangzhe-ctrl/ani-session-gateway/api/gen/session/v1"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/memory"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
	"google.golang.org/grpc/status"
)

type consoleAuthority struct{ calls int }

func (v *consoleAuthority) ValidateVM(context.Context, session.VMBinding) error {
	v.calls++
	return nil
}

func governanceContext(identity string, verified bool) context.Context {
	uri, _ := url.Parse(identity)
	info := credentials.TLSInfo{}
	info.State.PeerCertificates = []*x509.Certificate{{URIs: []*url.URL{uri}}}
	if verified {
		info.State.VerifiedChains = [][]*x509.Certificate{info.State.PeerCertificates}
	}
	return peer.NewContext(context.Background(), &peer.Peer{AuthInfo: info})
}

func TestVMConsoleRequiresExactVerifiedGovernanceAndPreservesReplay(t *testing.T) {
	base, _ := url.Parse("ws://127.0.0.1/api/v1/realtime")
	validator := &consoleAuthority{}
	manager, err := session.NewManager(memory.New(), session.ManagerConfig{VMValidator: validator, PublicWSBaseURL: base, TicketTTL: time.Minute, SessionMaxDuration: time.Minute, IdempotencyTTL: time.Minute, MaxActive: 2, MaxActivePerSubject: 2})
	if err != nil {
		t.Fatal(err)
	}
	server := NewSessionServer(manager)
	binding := &consolev1.VMConsoleBinding{TenantId: "tenant", VmId: "vm-id", Version: 2, ClusterId: "cluster", Namespace: "tenant-namespace", NamespaceUid: "ns-uid", VmName: "vm", VmUid: "vm-uid", VmiName: "vm", VmiUid: "vmi-uid"}
	request := &sessionv1.CreateSessionRequest{IdempotencyKey: "console-key", Principal: &sessionv1.Principal{TenantId: "tenant", SubjectId: "subject"}, Target: &sessionv1.Target{InstanceId: "vm-id", WorkloadName: "vm", WorkloadKind: sessionv1.WorkloadKind_WORKLOAD_KIND_VM, VmBinding: binding}, Mode: &sessionv1.CreateSessionRequest_VmConsole{VmConsole: &sessionv1.VMConsoleOptions{Protocol: sessionv1.VMConsoleOptions_PROTOCOL_VNC, RequestedProtocol: "novnc"}}}
	for _, ctx := range []context.Context{context.Background(), governanceContext("spiffe://ani.internal/service/ani-governance", false), governanceContext("spiffe://ani.internal/service/ani-session-gateway", true), governanceContext("spiffe://ani.internal/service/ani-governance/other", true)} {
		if _, err := server.CreateSession(ctx, request); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("untrusted issue code=%s", status.Code(err))
		}
		if _, err := server.GetSessionReplay(ctx, &sessionv1.GetSessionReplayRequest{}); status.Code(err) != codes.PermissionDenied {
			t.Fatalf("untrusted replay code=%s", status.Code(err))
		}
	}
	ctx := governanceContext("spiffe://ani.internal/service/ani-governance", true)
	issued, err := server.CreateSession(ctx, request)
	if err != nil {
		t.Fatal(err)
	}
	replayRequest := &sessionv1.GetSessionReplayRequest{IdempotencyKey: request.IdempotencyKey, Principal: request.Principal, InstanceId: "vm-id", ExpectedVersion: binding.Version}
	replayed, err := server.GetSessionReplay(ctx, replayRequest)
	if err != nil || !replayed.Replayed || replayed.SessionId != issued.SessionId || replayed.ConnectUrl != issued.ConnectUrl || validator.calls != 1 {
		t.Fatalf("replay changed original intent/validated again: %v", err)
	}
	replayRequest.IdempotencyKey = "missing"
	if _, err := server.GetSessionReplay(ctx, replayRequest); status.Code(err) != codes.NotFound {
		t.Fatalf("missing replay code=%s", status.Code(err))
	}
	replayRequest.IdempotencyKey = request.IdempotencyKey
	replayRequest.ExpectedVersion++
	if _, err := server.GetSessionReplay(ctx, replayRequest); status.Code(err) != codes.AlreadyExists {
		t.Fatalf("changed replay code=%s", status.Code(err))
	}
}
