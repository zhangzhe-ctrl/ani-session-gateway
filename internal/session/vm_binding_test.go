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

type vmAuthority struct {
	denied bool
	calls  int
}

func (a *vmAuthority) ValidateVM(context.Context, session.VMBinding) error {
	a.calls++
	if a.denied {
		return session.ErrFailedPrecondition
	}
	return nil
}

func vmRequest() session.Request {
	return session.Request{IdempotencyKey: "console-intent", TenantID: "tenant-a", SubjectID: "subject", InstanceID: "vm-id", WorkloadName: "vm-original", WorkloadKind: session.WorkloadVM, Mode: session.ModeVNC, RequestedProtocol: "novnc", VMBinding: session.VMBinding{Enabled: true, TenantID: "tenant-a", VMID: "vm-id", Version: 7, ClusterID: "cluster", Namespace: "frozen-namespace", VMName: "vm-original", VMUID: "vm-uid", VMIName: "vmi-original", VMIUID: "vmi-uid", NamespaceUID: "namespace-uid"}}
}

func vmManager(t *testing.T, store session.Store, authority session.VMValidator, now *time.Time) *session.Manager {
	t.Helper()
	base, _ := url.Parse("ws://127.0.0.1/api/v1/realtime")
	m, err := session.NewManager(store, session.ManagerConfig{VMValidator: authority, PublicWSBaseURL: base, TicketTTL: time.Minute, SessionMaxDuration: 15 * time.Minute, IdempotencyTTL: 15 * time.Minute, MaxActive: 5, MaxActivePerSubject: 2, Now: func() time.Time { return *now }})
	if err != nil {
		t.Fatal(err)
	}
	return m
}

func TestVMConsoleIdentitySurvivesClaimAndEveryBindingFieldAffectsFingerprint(t *testing.T) {
	now := time.Now().UTC()
	authority := &vmAuthority{}
	m := vmManager(t, memory.New(), authority, &now)
	r := vmRequest()
	issued, err := m.Issue(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	mutations := []func(*session.VMBinding){
		func(b *session.VMBinding) { b.TenantID = "other" },
		func(b *session.VMBinding) { b.VMID = "other" },
		func(b *session.VMBinding) { b.Version++ },
		func(b *session.VMBinding) { b.ClusterID = "other" },
		func(b *session.VMBinding) { b.Namespace = "other" },
		func(b *session.VMBinding) { b.VMName = "other" },
		func(b *session.VMBinding) { b.VMUID = "replacement" },
		func(b *session.VMBinding) { b.VMIName = "other" },
		func(b *session.VMBinding) { b.VMIUID = "replacement" },
		func(b *session.VMBinding) { b.NamespaceUID = "replacement" },
	}
	for i, mutate := range mutations {
		changed := r
		mutate(&changed.VMBinding)
		if session.Fingerprint(changed) == session.Fingerprint(r) {
			t.Fatalf("binding field %d was omitted from fingerprint", i)
		}
	}
	changed := r
	changed.VMBinding.VMIUID = "replacement"
	if _, err := m.Issue(t.Context(), changed); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("replacement replay error=%v", err)
	}
	access, err := m.Claim(t.Context(), issued.Session.ID, issued.Ticket)
	if err != nil || access.Target.VMBinding != r.VMBinding {
		t.Fatalf("claim lost original identity: %v", err)
	}
	// No IP, Agent or installation flag exists in this valid admission.
	if authority.calls != 2 {
		t.Fatalf("authority calls=%d", authority.calls)
	}
}

func TestVMConsoleRejectsMixedBindingsWrongKindAndNameOnlyFallback(t *testing.T) {
	now := time.Now().UTC()
	m := vmManager(t, memory.New(), &vmAuthority{}, &now)
	for _, mutate := range []func(*session.Request){
		func(r *session.Request) { r.VMBinding.NamespaceUID = "" },
		func(r *session.Request) { r.VMBinding.TenantID = "other" },
		func(r *session.Request) { r.VMBinding.VMID = "other" },
		func(r *session.Request) { r.VMBinding.VMName = "other" },
		func(r *session.Request) { r.Mode = session.ModeSerial },
		func(r *session.Request) { r.WorkloadKind = session.WorkloadContainer },
		func(r *session.Request) { r.InstanceBinding.Enabled = true },
		func(r *session.Request) { r.VMBinding = session.VMBinding{} },
	} {
		r := vmRequest()
		mutate(&r)
		if _, err := m.Issue(t.Context(), r); !errors.Is(err, session.ErrInvalidRequest) {
			t.Fatalf("invalid binding accepted: %v", err)
		}
	}
	m = vmManager(t, memory.New(), nil, &now)
	if _, err := m.Issue(t.Context(), vmRequest()); !errors.Is(err, session.ErrUnavailable) {
		t.Fatalf("missing authority error=%v", err)
	}
}

type replayOnlyStore struct {
	session.Store
	creates int
}

func (s *replayOnlyStore) CreateOrGet(ctx context.Context, key string, candidate session.Session) (session.Session, bool, error) {
	s.creates++
	return s.Store.CreateOrGet(ctx, key, candidate)
}

func TestVMReplayIsReadOnlyEvenWhenCurrentDeleteFenceRejectsNewIntent(t *testing.T) {
	now := time.Now().UTC()
	authority := &vmAuthority{}
	store := &replayOnlyStore{Store: memory.New()}
	m := vmManager(t, store, authority, &now)
	r := vmRequest()
	issued, err := m.Issue(t.Context(), r)
	if err != nil {
		t.Fatal(err)
	}
	authority.denied = true
	replay := session.ReplayRequest{IdempotencyKey: r.IdempotencyKey, TenantID: r.TenantID, SubjectID: r.SubjectID, InstanceID: r.InstanceID, ExpectedVersion: r.VMBinding.Version}
	replayed, err := m.Replay(t.Context(), replay)
	if err != nil || !replayed.Replayed || replayed.Session.ID != issued.Session.ID || replayed.Ticket != issued.Ticket {
		t.Fatalf("original intent was not replayed: %v", err)
	}
	missing := replay
	missing.IdempotencyKey = "new-session-key"
	if _, err := m.Replay(t.Context(), missing); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("missing key created session: %v", err)
	}
	other := replay
	other.TenantID = "other-tenant"
	if _, err := m.Replay(t.Context(), other); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("cross tenant replay error=%v", err)
	}
	other = replay
	other.ExpectedVersion++
	if _, err := m.Replay(t.Context(), other); !errors.Is(err, session.ErrConflict) {
		t.Fatalf("changed intent replay error=%v", err)
	}
	if store.creates != 1 || authority.calls != 1 {
		t.Fatalf("replay called mutating/validation path: creates=%d validations=%d", store.creates, authority.calls)
	}
	if _, err := m.Claim(t.Context(), issued.Session.ID, issued.Ticket); err != nil {
		t.Fatal(err)
	}
	if _, err := m.Replay(t.Context(), replay); !errors.Is(err, session.ErrFailedPrecondition) {
		t.Fatalf("claimed replay error=%v", err)
	}
	now = now.Add(31 * time.Minute)
	if _, err := m.Replay(t.Context(), replay); !errors.Is(err, session.ErrNotFound) {
		t.Fatalf("expired tombstone replay error=%v", err)
	}
}
