package resourceruntime

import (
	"context"
	"errors"
	sandboxv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/sandbox/terminal/v1"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"testing"
	"time"
)

type sandboxClient struct {
	sandboxv1.SandboxTerminalServiceClient
	request *sandboxv1.SandboxTerminalBinding
	valid   bool
	err     error
	md      metadata.MD
}

func (c *sandboxClient) ValidateTerminalSession(ctx context.Context, b *sandboxv1.SandboxTerminalBinding, _ ...grpc.CallOption) (*sandboxv1.TerminalSessionValidation, error) {
	c.request = b
	c.md, _ = metadata.FromOutgoingContext(ctx)
	return &sandboxv1.TerminalSessionValidation{Valid: c.valid}, c.err
}
func TestSandboxValidationForwardsIdentityAndStripsCallerMetadata(t *testing.T) {
	b := session.SandboxBinding{Enabled: true, TenantID: "t", SandboxID: "s", Version: 2, ClusterID: "c", Namespace: "n", NamespaceUID: "nuid", PodName: "p", PodUID: "puid", ContainerName: "sandbox", Shell: "/bin/sh"}
	c := &sandboxClient{valid: true}
	v := &Validator{sandboxClient: c, requestTimeout: time.Second}
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("authorization", "secret"))
	if err := v.ValidateSandbox(ctx, b); err != nil {
		t.Fatal(err)
	}
	want := &sandboxv1.SandboxTerminalBinding{TenantId: "t", SandboxId: "s", Version: 2, ClusterId: "c", Namespace: "n", NamespaceUid: "nuid", PodName: "p", PodUid: "puid", ContainerName: "sandbox", Shell: "/bin/sh"}
	if !proto.Equal(c.request, want) || len(c.md) != 0 {
		t.Fatal("identity lost or untrusted metadata forwarded")
	}
	for _, code := range []codes.Code{codes.PermissionDenied, codes.FailedPrecondition, codes.NotFound} {
		c.err = status.Error(code, "denied")
		if !errors.Is(v.ValidateSandbox(t.Context(), b), session.ErrFailedPrecondition) {
			t.Fatalf("failed to fail closed for %s", code)
		}
	}
	c.err = status.Error(codes.Unavailable, "offline")
	if !errors.Is(v.ValidateSandbox(t.Context(), b), session.ErrUnavailable) {
		t.Fatal("unavailable accepted")
	}
	c.err = nil
	c.valid = false
	if !errors.Is(v.ValidateSandbox(t.Context(), b), session.ErrFailedPrecondition) {
		t.Fatal("invalid accepted")
	}
}
