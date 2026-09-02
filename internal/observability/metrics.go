package observability

import (
	"time"

	"github.com/prometheus/client_golang/prometheus"
)

type SessionMetrics struct {
	create        *prometheus.CounterVec
	active        *prometheus.GaugeVec
	duration      *prometheus.HistogramVec
	bytes         *prometheus.CounterVec
	claim         *prometheus.CounterVec
	runtimeErrors *prometheus.CounterVec
}

func newSessionMetrics(registry prometheus.Registerer) *SessionMetrics {
	metrics := &SessionMetrics{
		create:        prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ani_session_create_total", Help: "Session creation attempts by mode and result."}, []string{"mode", "result"}),
		active:        prometheus.NewGaugeVec(prometheus.GaugeOpts{Name: "ani_session_active", Help: "Currently connected sessions by mode."}, []string{"mode"}),
		duration:      prometheus.NewHistogramVec(prometheus.HistogramOpts{Name: "ani_session_duration_seconds", Help: "Connected session duration by mode."}, []string{"mode"}),
		bytes:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ani_session_bytes_total", Help: "Session payload bytes by mode and direction."}, []string{"mode", "direction"}),
		claim:         prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ani_session_claim_total", Help: "Ticket claim attempts by result."}, []string{"result"}),
		runtimeErrors: prometheus.NewCounterVec(prometheus.CounterOpts{Name: "ani_session_runtime_errors_total", Help: "Runtime failures by mode and bounded code."}, []string{"mode", "code"}),
	}
	registry.MustRegister(metrics.create, metrics.active, metrics.duration, metrics.bytes, metrics.claim, metrics.runtimeErrors)
	return metrics
}

func (m *SessionMetrics) SessionCreated(mode, result string) {
	if m != nil {
		m.create.WithLabelValues(boundedMode(mode), result).Inc()
	}
}

func (m *SessionMetrics) SessionClaimed(result string) {
	if m != nil {
		m.claim.WithLabelValues(result).Inc()
	}
}

func (m *SessionMetrics) Connected(mode string) {
	if m != nil {
		m.active.WithLabelValues(boundedMode(mode)).Inc()
	}
}

func (m *SessionMetrics) Closed(mode string, duration time.Duration) {
	if m != nil {
		mode = boundedMode(mode)
		m.active.WithLabelValues(mode).Dec()
		m.duration.WithLabelValues(mode).Observe(duration.Seconds())
	}
}

func (m *SessionMetrics) AddBytes(mode, direction string, count int) {
	if m != nil && count > 0 {
		m.bytes.WithLabelValues(boundedMode(mode), direction).Add(float64(count))
	}
}

func (m *SessionMetrics) RuntimeError(mode, code string) {
	if m != nil {
		m.runtimeErrors.WithLabelValues(boundedMode(mode), code).Inc()
	}
}

func boundedMode(mode string) string {
	switch mode {
	case "exec", "serial", "vnc":
		return mode
	default:
		return "unknown"
	}
}
