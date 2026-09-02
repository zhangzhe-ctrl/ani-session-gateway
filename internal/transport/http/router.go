package httptransport

import (
	"net/http"

	"github.com/go-chi/chi/v5"
	"github.com/go-chi/chi/v5/middleware"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/observability"
)

func NewRouter(probes *observability.Handler) chi.Router {
	router := chi.NewRouter()
	router.Use(middleware.Recoverer)
	router.NotFound(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "not found", http.StatusNotFound) })
	router.MethodNotAllowed(func(w http.ResponseWriter, _ *http.Request) {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	})
	router.Get("/healthz", traced("http.healthz", probes.Health))
	router.Get("/readyz", traced("http.readyz", probes.Ready))
	router.Method(http.MethodGet, "/metrics", tracedHandler("http.metrics", probes.Metrics()))
	return router
}

func traced(name string, next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, request *http.Request) {
		ctx, span := observability.StartSpan(request.Context(), name)
		defer span.End()
		next(w, request.WithContext(ctx))
	}
}
func tracedHandler(name string, next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		ctx, span := observability.StartSpan(request.Context(), name)
		defer span.End()
		next.ServeHTTP(w, request.WithContext(ctx))
	})
}
