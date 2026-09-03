package observability

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/websocket/connectedsession"
)

func TestMinimumSessionMetricsAreExported(t *testing.T) {
	handler := NewHandler()
	handler.RegisterStore("redis", false)
	metrics := handler.SessionMetrics()
	metrics.SessionCreated("exec", "success")
	metrics.SessionClaimed("success")
	metrics.Connected("exec")
	metrics.AddBytes("exec", "in", 3)
	metrics.AddBytes("exec", "out", 5)
	metrics.RuntimeError("exec", "timeout")
	metrics.Closed("exec", 250*time.Millisecond)
	recorder := httptest.NewRecorder()
	handler.Metrics().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if recorder.Code != http.StatusOK {
		t.Fatalf("metrics status=%d", recorder.Code)
	}
	body := recorder.Body.String()
	for _, name := range []string{"ani_session_create_total", "ani_session_active", "ani_session_duration_seconds", "ani_session_bytes_total", "ani_session_claim_total", "ani_session_runtime_errors_total", "ani_session_store_info", "ani_session_store_degraded"} {
		if !strings.Contains(body, name) {
			t.Fatalf("metrics output omits %s", name)
		}
	}
	if strings.Contains(body, "ticket=") || strings.Contains(body, "ticket=\"") || strings.Contains(body, "credential=") || strings.Contains(body, "payload=") {
		t.Fatal("metrics contain a forbidden sensitive label")
	}
}

func TestConnectedSessionObservationExportsOneBoundedOutcome(t *testing.T) {
	handler := NewHandler()
	observer := NewConnectedSessionObserver(handler.SessionMetrics())
	observation := observer.Open(context.Background(), connectedsession.SafeFacts{
		SessionID: "session-a", TenantID: "tenant-a", SubjectID: "subject-a", InstanceID: "instance-a", Mode: session.ModeExec,
	})
	observation.Connected(time.Now())
	observation.Connected(time.Now())
	finish := connectedsession.Finish{Outcome: connectedsession.OutcomeClientClosed, Connected: true, Duration: 250 * time.Millisecond, BytesIn: 3, BytesOut: 5}
	observation.Finish(finish)
	observation.Finish(finish)
	failed := observer.Open(context.Background(), connectedsession.SafeFacts{SessionID: "session-b", Mode: session.ModeVNC})
	failed.Finish(connectedsession.Finish{Outcome: connectedsession.OutcomeRuntimeUnavailable})

	recorder := httptest.NewRecorder()
	handler.Metrics().ServeHTTP(recorder, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := recorder.Body.String()
	for _, want := range []string{
		`ani_session_active{mode="exec"} 0`,
		`ani_session_bytes_total{direction="in",mode="exec"} 3`,
		`ani_session_bytes_total{direction="out",mode="exec"} 5`,
		`ani_session_end_total{mode="exec",outcome="client_closed"} 1`,
		`ani_session_end_total{mode="vnc",outcome="runtime_unavailable"} 1`,
		`ani_session_runtime_errors_total{code="open_failed",mode="vnc"} 1`,
	} {
		if !strings.Contains(body, want) {
			t.Fatalf("metrics output omits %q\n%s", want, body)
		}
	}
	if strings.Contains(body, `ani_session_runtime_errors_total{code="client_closed"`) {
		t.Fatal("client close was classified as a runtime error")
	}
}
