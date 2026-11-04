package session_test

import (
	"context"
	"errors"
	"net/url"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/memory"
)

type sandboxAuthority struct{ denied bool }

func (a *sandboxAuthority) ValidateSandbox(context.Context, session.SandboxBinding) error {
	if a.denied {
		return session.ErrFailedPrecondition
	}
	return nil
}
func standaloneSandboxRequest() session.Request {
	return session.Request{IdempotencyKey: "sandbox-intent", TenantID: "tenant-a", SubjectID: "subject", SandboxID: "sandbox-id", WorkloadName: "sandbox-sandbox-id", WorkloadKind: session.WorkloadSandbox, Mode: session.ModeExec, Container: "sandbox", Command: []string{"/bin/sh"}, TTY: true, Rows: 24, Cols: 80, SandboxBinding: session.SandboxBinding{Enabled: true, TenantID: "tenant-a", SandboxID: "sandbox-id", Version: 1, ClusterID: "cluster", Namespace: "tenant-tenant-a", NamespaceUID: "namespace-uid", PodName: "sandbox-sandbox-id", PodUID: "pod-uid", ContainerName: "sandbox", Shell: "/bin/sh"}}
}
func TestStandaloneSandboxIdentityAndAuthority(t *testing.T) {
	base, _ := url.Parse("ws://127.0.0.1/api/v1/realtime")
	authority := &sandboxAuthority{}
	m, err := session.NewManager(memory.New(), session.ManagerConfig{SandboxValidator: authority, PublicWSBaseURL: base, TicketTTL: time.Minute, SessionMaxDuration: time.Minute, IdempotencyTTL: time.Minute, MaxActive: 5, MaxActivePerSubject: 2})
	if err != nil {
		t.Fatal(err)
	}
	r := standaloneSandboxRequest()
	issued, err := m.Issue(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*session.SandboxBinding){func(b *session.SandboxBinding) { b.TenantID = "other" }, func(b *session.SandboxBinding) { b.SandboxID = "other" }, func(b *session.SandboxBinding) { b.Version++ }, func(b *session.SandboxBinding) { b.ClusterID = "other" }, func(b *session.SandboxBinding) { b.Namespace = "other" }, func(b *session.SandboxBinding) { b.NamespaceUID = "other" }, func(b *session.SandboxBinding) { b.PodName = "other" }, func(b *session.SandboxBinding) { b.PodUID = "other" }, func(b *session.SandboxBinding) { b.ContainerName = "other" }, func(b *session.SandboxBinding) { b.Shell = "other" }}
	for i, mutate := range mutations {
		changed := r
		mutate(&changed.SandboxBinding)
		if session.Fingerprint(changed) == session.Fingerprint(r) {
			t.Fatalf("binding field %d missing from fingerprint", i)
		}
	}
	changed := r
	changed.SandboxBinding.PodUID = "replacement"
	if _, err = m.Issue(t.Context(), changed); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("replacement replay: %v", err)
	}
	access, err := m.Claim(t.Context(), issued.Session.ID, issued.Ticket)
	if err != nil || access.Identity.SandboxID != r.SandboxID || access.Identity.InstanceID != "" || access.Target.SandboxBinding != r.SandboxBinding {
		t.Fatalf("claim lost sandbox identity: %v", err)
	}
	authority.denied = true
	r.IdempotencyKey = "new-intent"
	if _, err = m.Issue(t.Context(), r); !errors.Is(err, session.ErrFailedPrecondition) {
		t.Fatalf("revoked sandbox accepted: %v", err)
	}
}
func TestStandaloneSandboxRejectsMixedAndMismatchedBindings(t *testing.T) {
	base, _ := url.Parse("ws://127.0.0.1/api/v1/realtime")
	m, err := session.NewManager(memory.New(), session.ManagerConfig{SandboxValidator: &sandboxAuthority{}, PublicWSBaseURL: base, TicketTTL: time.Minute, SessionMaxDuration: time.Minute, IdempotencyTTL: time.Minute, MaxActive: 5, MaxActivePerSubject: 2})
	if err != nil {
		t.Fatal(err)
	}
	for _, mutate := range []func(*session.Request){func(r *session.Request) { r.InstanceID = "legacy" }, func(r *session.Request) { r.InstanceBinding.Enabled = true }, func(r *session.Request) { r.VMBinding.Enabled = true }, func(r *session.Request) { r.SandboxBinding.TenantID = "other" }, func(r *session.Request) { r.SandboxBinding.SandboxID = "other" }, func(r *session.Request) { r.SandboxBinding.NamespaceUID = "" }, func(r *session.Request) { r.SandboxBinding.PodName = "other" }, func(r *session.Request) { r.Container = "other" }, func(r *session.Request) { r.SandboxBinding.Shell = "/bin/bash" }, func(r *session.Request) { r.WorkloadKind = session.WorkloadVM }, func(r *session.Request) { r.SandboxBinding = session.SandboxBinding{} }} {
		r := standaloneSandboxRequest()
		mutate(&r)
		if _, err := m.Issue(t.Context(), r); !errors.Is(err, session.ErrInvalidRequest) {
			t.Fatalf("invalid target accepted: %+v %v", r, err)
		}
	}
	m, err = session.NewManager(memory.New(), session.ManagerConfig{PublicWSBaseURL: base, TicketTTL: time.Minute, SessionMaxDuration: time.Minute, IdempotencyTTL: time.Minute, MaxActive: 5, MaxActivePerSubject: 2})
	if err != nil {
		t.Fatal(err)
	}
	if _, err = m.Issue(t.Context(), standaloneSandboxRequest()); !errors.Is(err, session.ErrUnavailable) {
		t.Fatalf("missing authority accepted: %v", err)
	}
}
