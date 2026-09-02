package observability

import (
	"context"
	"net/http"
	"sync/atomic"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/promhttp"
)

type Handler struct {
	ready     atomic.Bool
	registry  *prometheus.Registry
	check     func(context.Context) error
	storeMode string
	degraded  bool
	sessions  *SessionMetrics
}

func NewHandler() *Handler {
	registry := prometheus.NewRegistry()
	return &Handler{registry: registry, sessions: newSessionMetrics(registry)}
}
func (h *Handler) SetReady(value bool)                                  { h.ready.Store(value) }
func (h *Handler) SetDependencyCheck(check func(context.Context) error) { h.check = check }
func (h *Handler) Registry() *prometheus.Registry                       { return h.registry }
func (h *Handler) SessionMetrics() *SessionMetrics                      { return h.sessions }

func (h *Handler) RegisterStore(mode string, degraded bool) {
	h.storeMode, h.degraded = mode, degraded
	info := prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ani_session_store_info", Help: "Selected session store mode."}, []string{"mode"})
	info.WithLabelValues(mode).Set(1)
	degradedGauge := prometheus.NewGauge(prometheus.GaugeOpts{Name: "ani_session_store_degraded", Help: "Whether the selected store is local-only and degraded."})
	if degraded {
		degradedGauge.Set(1)
	}
	h.registry.MustRegister(info, degradedGauge)
}

func (h *Handler) Health(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	_, _ = w.Write([]byte("ok\n"))
}
func (h *Handler) Ready(w http.ResponseWriter, request *http.Request) {
	if !h.ready.Load() || (h.check != nil && h.check(request.Context()) != nil) {
		http.Error(w, "not ready", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "text/plain; charset=utf-8")
	if h.degraded {
		_, _ = w.Write([]byte("ready mode=" + h.storeMode + " local=true degraded=true\n"))
		return
	}
	_, _ = w.Write([]byte("ready mode=" + h.storeMode + " degraded=false\n"))
}
func (h *Handler) Metrics() http.Handler {
	return promhttp.HandlerFor(h.registry, promhttp.HandlerOpts{})
}
