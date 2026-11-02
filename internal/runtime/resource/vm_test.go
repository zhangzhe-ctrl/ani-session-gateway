package resourceruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	terminalv1 "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/terminal/v1"
	consolev1 "github.com/zhangzhe-ctrl/ani-resource-service/api/vm/console/v1"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
)

type vmConsoleClient struct {
	request  *consolev1.VMConsoleBinding
	valid    bool
	err      error
	metadata metadata.MD
}

type delayedVMConsoleClient struct {
	delay time.Duration
}

type delayedTerminalClient struct {
	terminalv1.InstanceTerminalServiceClient
	delay time.Duration
}

func (c delayedTerminalClient) ValidateTerminalTarget(ctx context.Context, _ *terminalv1.TerminalTarget, _ ...grpc.CallOption) (*terminalv1.TerminalValidation, error) {
	timer := time.NewTimer(c.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return &terminalv1.TerminalValidation{Valid: true}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestResourceContainerValidationUsesConfiguredTimeout(t *testing.T) {
	validator := &Validator{client: delayedTerminalClient{delay: 3200 * time.Millisecond}, requestTimeout: 5 * time.Second}
	if err := validator.Validate(t.Context(), "tenant", "instance", session.InstanceBinding{Enabled: true}); err != nil {
		t.Fatalf("healthy container validation taking longer than three seconds failed: %v", err)
	}
	validator.requestTimeout = 20 * time.Millisecond
	if err := validator.Validate(t.Context(), "tenant", "instance", session.InstanceBinding{Enabled: true}); !errors.Is(err, session.ErrUnavailable) {
		t.Fatalf("configured container deadline must fail closed: %v", err)
	}
}

func (c delayedVMConsoleClient) ValidateConsoleSession(ctx context.Context, _ *consolev1.VMConsoleBinding, _ ...grpc.CallOption) (*consolev1.ConsoleSessionValidation, error) {
	timer := time.NewTimer(c.delay)
	defer timer.Stop()
	select {
	case <-timer.C:
		return &consolev1.ConsoleSessionValidation{Valid: true}, nil
	case <-ctx.Done():
		return nil, ctx.Err()
	}
}

func TestResourceVMConsoleAllowsSlowValidationAndHonorsCallerCancellation(t *testing.T) {
	binding := session.VMBinding{Enabled: true, TenantID: "tenant", VMID: "vm-id", Version: 1, ClusterID: "cluster", Namespace: "namespace", VMName: "vm-name", VMUID: "vm-uid", VMIName: "vmi-name", VMIUID: "vmi-uid", NamespaceUID: "namespace-uid"}
	validator := &Validator{vmClient: delayedVMConsoleClient{delay: 3200 * time.Millisecond}, requestTimeout: 5 * time.Second}
	if err := validator.ValidateVM(t.Context(), binding); err != nil {
		t.Fatalf("healthy Resource validation taking longer than three seconds failed: %v", err)
	}
	validator.requestTimeout = 20 * time.Millisecond
	if err := validator.ValidateVM(t.Context(), binding); !errors.Is(err, session.ErrUnavailable) {
		t.Fatalf("configured VM deadline must fail closed: %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := validator.ValidateVM(ctx, binding); !errors.Is(err, session.ErrUnavailable) {
		t.Fatalf("cancelled caller must fail closed: %v", err)
	}
}

func (c *vmConsoleClient) ValidateConsoleSession(ctx context.Context, binding *consolev1.VMConsoleBinding, _ ...grpc.CallOption) (*consolev1.ConsoleSessionValidation, error) {
	c.request = binding
	c.metadata, _ = metadata.FromOutgoingContext(ctx)
	return &consolev1.ConsoleSessionValidation{Valid: c.valid}, c.err
}

func TestResourceVMConsoleForwardsAllFrozenIdentityAndStripsCallerMetadata(t *testing.T) {
	binding := session.VMBinding{Enabled: true, TenantID: "tenant", VMID: "vm-id", Version: 12, ClusterID: "cluster", Namespace: "namespace", VMName: "vm-name", VMUID: "vm-uid", VMIName: "vmi-name", VMIUID: "vmi-uid", NamespaceUID: "namespace-uid"}
	client := &vmConsoleClient{valid: true}
	validator := &Validator{vmClient: client, requestTimeout: 30 * time.Second}
	ctx := metadata.NewOutgoingContext(t.Context(), metadata.Pairs("x-tenant-id", "spoofed", "authorization", "caller-metadata"))
	if err := validator.ValidateVM(ctx, binding); err != nil {
		t.Fatal(err)
	}
	want := &consolev1.VMConsoleBinding{TenantId: "tenant", VmId: "vm-id", Version: 12, ClusterId: "cluster", Namespace: "namespace", VmName: "vm-name", VmUid: "vm-uid", VmiName: "vmi-name", VmiUid: "vmi-uid", NamespaceUid: "namespace-uid"}
	if !proto.Equal(client.request, want) {
		t.Fatal("Resource did not receive the complete frozen identity")
	}
	if len(client.metadata) != 0 {
		t.Fatal("browser or caller metadata reached the trusted Resource channel")
	}
	for _, code := range []codes.Code{codes.FailedPrecondition, codes.NotFound, codes.PermissionDenied} {
		client.err = status.Error(code, "target unavailable")
		if err := validator.ValidateVM(t.Context(), binding); !errors.Is(err, session.ErrFailedPrecondition) {
			t.Fatalf("code=%s err=%v", code, err)
		}
	}
	client.err = status.Error(codes.Unavailable, "provider unavailable")
	if err := validator.ValidateVM(t.Context(), binding); !errors.Is(err, session.ErrUnavailable) {
		t.Fatal(err)
	}
}
