package observability

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
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
