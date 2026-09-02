package session_test

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/memory"
	"go.opentelemetry.io/otel"
	sdktrace "go.opentelemetry.io/otel/sdk/trace"
	"go.opentelemetry.io/otel/sdk/trace/tracetest"
)

func TestManagerIdempotencyEncryptionAndClaim(t *testing.T) {
	now := time.Unix(1700010000, 0).UTC()
	store := memory.New()
	manager := newManager(t, store, &now)
	request := validRequest()
	first, err := manager.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	request.RequestID = "a-different-request-id"
	restartedManager := newManager(t, store, &now)
	replayed, err := restartedManager.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if !replayed.Replayed || replayed.Session.ID != first.Session.ID || replayed.Ticket != first.Ticket {
		t.Fatalf("replay changed result: first=%#v replay=%#v", first, replayed)
	}
	if strings.Contains(fmt.Sprintf("%#v", store), first.Ticket) {
		t.Fatal("memory store contains plaintext ticket")
	}
	if !strings.Contains(manager.ConnectURL(first), "ticket=") {
		t.Fatal("connect URL omitted ticket")
	}
	lease, err := manager.Claim(context.Background(), first.Session.ID, first.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Claim(context.Background(), first.Session.ID, first.Ticket); !errors.Is(err, session.ErrFailedPrecondition) {
		t.Fatalf("ticket replay error=%v", err)
	}
	if _, err := manager.Issue(context.Background(), request); !errors.Is(err, session.ErrFailedPrecondition) {
		t.Fatalf("idempotency replay after claim error=%v", err)
	}
	if err := manager.Close(context.Background(), first.Session.ID, lease.ID, "normal"); err != nil {
		t.Fatal(err)
	}
}

func TestManagerFingerprintCoversOptionsButNotRequestID(t *testing.T) {
	base := validRequest()
	changedID := base
	changedID.RequestID = "different"
	if session.Fingerprint(base) != session.Fingerprint(changedID) {
		t.Fatal("request ID affected fingerprint")
	}
	changedCommand := base
	changedCommand.Command = []string{"/bin/bash"}
	if session.Fingerprint(base) == session.Fingerprint(changedCommand) {
		t.Fatal("command missing from fingerprint")
	}
	changedTenant := base
	changedTenant.TenantID = "other"
	if session.Fingerprint(base) == session.Fingerprint(changedTenant) {
		t.Fatal("tenant missing from fingerprint")
	}
}

func TestManagerExpiredTicketKeepsTombstone(t *testing.T) {
	now := time.Unix(1700011000, 0).UTC()
	store := memory.New()
	manager := newManager(t, store, &now)
	request := validRequest()
	issued, err := manager.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(61 * time.Second)
	if _, err := manager.Claim(context.Background(), issued.Session.ID, issued.Ticket); !errors.Is(err, session.ErrFailedPrecondition) {
		t.Fatalf("expired claim error=%v", err)
	}
	if _, err := manager.Issue(context.Background(), request); !errors.Is(err, session.ErrFailedPrecondition) {
		t.Fatalf("expired idempotency replay error=%v", err)
	}
}

func TestManagerSpansExcludeRequestAndTicketMaterial(t *testing.T) {
	recorder := tracetest.NewSpanRecorder()
	provider := sdktrace.NewTracerProvider(sdktrace.WithSpanProcessor(recorder))
	otel.SetTracerProvider(provider)
	t.Cleanup(func() { _ = provider.Shutdown(context.Background()) })
	now := time.Unix(1700012000, 0).UTC()
	manager := newManager(t, memory.New(), &now)
	request := validRequest()
	request.RequestID = "sensitive-request-value"
	request.SubjectID = "sensitive-subject-value"
	issued, err := manager.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Claim(context.Background(), issued.Session.ID, issued.Ticket); err != nil {
		t.Fatal(err)
	}
	for _, recorded := range recorder.Ended() {
		if len(recorded.Attributes()) != 0 || len(recorded.Events()) != 0 {
			t.Fatalf("sensitive-capable span contains attributes/events: %s", recorded.Name())
		}
		if strings.Contains(recorded.Name(), request.RequestID) || strings.Contains(recorded.Name(), request.SubjectID) || strings.Contains(recorded.Name(), issued.Ticket) {
			t.Fatalf("span name contains sensitive material: %s", recorded.Name())
		}
	}
}

func newManager(t *testing.T, store session.Store, now *time.Time) *session.Manager {
	t.Helper()
	base, _ := url.Parse("ws://127.0.0.1:30081/api/v1/realtime")
	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	manager, err := session.NewManager(store, session.ManagerConfig{PublicWSBaseURL: base, TicketKey: key, TicketTTL: time.Minute, SessionMaxDuration: 15 * time.Minute, IdempotencyTTL: 15 * time.Minute, MaxActive: 100, MaxActivePerSubject: 5, Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	return manager
}

func validRequest() session.Request {
	return session.Request{RequestID: "request", IdempotencyKey: "idem", TenantID: "tenant-a", SubjectID: "subject-a", InstanceID: "instance-a", WorkloadName: "workload-a", WorkloadKind: session.WorkloadContainer, Mode: session.ModeExec, Command: []string{"/bin/sh"}, TTY: true, Rows: 30, Cols: 120}
}
