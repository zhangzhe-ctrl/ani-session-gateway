package session_test

import (
	"context"
	"errors"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/memory"
	"net/url"
	"testing"
	"time"
)

type instanceAuthority struct{ stopped bool }

func (a *instanceAuthority) Validate(context.Context, string, string, session.InstanceBinding) error {
	if a.stopped {
		return session.ErrFailedPrecondition
	}
	return nil
}
func TestManagedInstanceTicketBindsOriginalTarget(t *testing.T) {
	url, _ := url.Parse("ws://127.0.0.1/api/v1/realtime")
	authority := &instanceAuthority{}
	m, e := session.NewManager(memory.New(), session.ManagerConfig{InstanceValidator: authority, PublicWSBaseURL: url, TicketTTL: time.Minute, SessionMaxDuration: 15 * time.Minute, IdempotencyTTL: 15 * time.Minute, MaxActive: 5, MaxActivePerSubject: 2})
	if e != nil {
		t.Fatal(e)
	}
	r := validRequest()
	r.Container = "workspace"
	r.InstanceBinding = session.InstanceBinding{Enabled: true, Version: 1, ClusterID: "cluster", Namespace: "saved-tenant", NotebookName: "original", NotebookUID: "notebook-uid", PodName: "original-0", PodUID: "pod-original", ContainerName: "workspace", Shell: "/bin/sh"}
	issued, e := m.Issue(context.Background(), r)
	if e != nil {
		t.Fatal(e)
	}
	changed := r
	changed.InstanceBinding.PodUID = "replacement"
	if _, e = m.Issue(context.Background(), changed); !errors.Is(e, session.ErrConflict) {
		t.Fatal("replacement target replayed", e)
	}
	access, e := m.Claim(context.Background(), issued.Session.ID, issued.Ticket)
	if e != nil || access.Target.InstanceBinding != r.InstanceBinding {
		t.Fatal("claim lost original target", e)
	}
	authority.stopped = true
	r.IdempotencyKey = "new-stop-ticket"
	if _, e = m.Issue(context.Background(), r); !errors.Is(e, session.ErrFailedPrecondition) {
		t.Fatal("stopped instance issued ticket", e)
	}
}
