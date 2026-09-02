package observability

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestHealthAndReadiness(t *testing.T) {
	h := NewHandler()
	for _, tc := range []struct {
		handler http.HandlerFunc
		want    int
	}{{h.Health, 200}, {h.Ready, 503}} {
		r := httptest.NewRequest(http.MethodGet, "/", nil)
		w := httptest.NewRecorder()
		tc.handler(w, r)
		if w.Code != tc.want {
			t.Fatalf("got %d want %d", w.Code, tc.want)
		}
	}
	w := httptest.NewRecorder()
	h.Metrics().ServeHTTP(w, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	if w.Code != 200 {
		t.Fatalf("metrics status: %d", w.Code)
	}
	h.RegisterStore("memory", true)
	h.SetDependencyCheck(func(context.Context) error { return nil })
	h.SetReady(true)
	r := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	w = httptest.NewRecorder()
	h.Ready(w, r)
	if w.Code != 200 {
		t.Fatalf("ready status: %d", w.Code)
	}
	if !strings.Contains(w.Body.String(), "local=true degraded=true") {
		t.Fatalf("memory readiness omitted local/degraded: %q", w.Body.String())
	}
	h.SetDependencyCheck(func(context.Context) error { return errors.New("down") })
	w = httptest.NewRecorder()
	h.Ready(w, r)
	if w.Code != 503 {
		t.Fatalf("dependency failure status: %d", w.Code)
	}
}
