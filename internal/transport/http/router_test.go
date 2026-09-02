package httptransport

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/observability"
)

func TestRouterMethodsNotFoundAndRecovery(t *testing.T) {
	probes := observability.NewHandler()
	probes.RegisterStore("memory", true)
	probes.SetReady(true)
	router := NewRouter(probes)
	router.Get("/panic", func(http.ResponseWriter, *http.Request) { panic("expected test panic") })
	for _, tc := range []struct {
		method, path string
		want         int
	}{{http.MethodGet, "/healthz", 200}, {http.MethodPost, "/healthz", 405}, {http.MethodGet, "/missing", 404}, {http.MethodGet, "/panic", 500}} {
		w := httptest.NewRecorder()
		router.ServeHTTP(w, httptest.NewRequest(tc.method, tc.path, nil))
		if w.Code != tc.want {
			t.Fatalf("%s %s: got %d want %d", tc.method, tc.path, w.Code, tc.want)
		}
	}
}
