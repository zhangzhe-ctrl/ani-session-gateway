package observability

import (
	"context"
	"log/slog"
	"sync"
	"time"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/websocket/connectedsession"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/trace"
)

type ConnectedSessionObserver struct {
	metrics *SessionMetrics
}

func NewConnectedSessionObserver(metrics *SessionMetrics) *ConnectedSessionObserver {
	return &ConnectedSessionObserver{metrics: metrics}
}

func (o *ConnectedSessionObserver) Open(ctx context.Context, facts connectedsession.SafeFacts) connectedsession.Observation {
	_, span := StartSpan(ctx, "connected_session.run")
	span.SetAttributes(
		attribute.String("session.id", facts.SessionID),
		attribute.String("session.mode", boundedMode(string(facts.Mode))),
	)
	return &connectedSessionObservation{metrics: o.metrics, facts: facts, span: span}
}

type connectedSessionObservation struct {
	metrics       *SessionMetrics
	facts         connectedsession.SafeFacts
	span          trace.Span
	connectedOnce sync.Once
	finishOnce    sync.Once
	mu            sync.Mutex
	connected     bool
}

func (o *connectedSessionObservation) Connected(_ time.Time) {
	o.connectedOnce.Do(func() {
		o.mu.Lock()
		o.connected = true
		o.mu.Unlock()
		o.metrics.Connected(string(o.facts.Mode))
		slog.Info("session_connected", o.safeLogArgs()...)
	})
}

func (o *connectedSessionObservation) Finish(finish connectedsession.Finish) {
	o.finishOnce.Do(func() {
		o.mu.Lock()
		connected := o.connected
		o.mu.Unlock()
		if connected {
			o.metrics.Closed(string(o.facts.Mode), finish.Duration)
		}
		o.metrics.AddBytes(string(o.facts.Mode), "in", finish.BytesIn)
		o.metrics.AddBytes(string(o.facts.Mode), "out", finish.BytesOut)
		o.metrics.Ended(string(o.facts.Mode), boundedOutcome(finish.Outcome))
		switch finish.Outcome {
		case connectedsession.OutcomeRuntimeUnavailable:
			o.metrics.RuntimeError(string(o.facts.Mode), "open_failed")
		case connectedsession.OutcomeRuntimeFailed:
			o.metrics.RuntimeError(string(o.facts.Mode), "stream_failed")
		}
		o.span.SetAttributes(
			attribute.String("session.outcome", boundedOutcome(finish.Outcome)),
			attribute.Bool("session.connected", connected),
		)
		o.span.End()
		args := append(o.safeLogArgs(), "duration_ms", finish.Duration.Milliseconds(), "outcome", boundedOutcome(finish.Outcome), "bytes_in", finish.BytesIn, "bytes_out", finish.BytesOut)
		if connected {
			slog.Info("session_closed", args...)
		} else {
			slog.Warn("session_failed", args...)
		}
	})
}

func (o *connectedSessionObservation) safeLogArgs() []any {
	return []any{
		"session_id", o.facts.SessionID,
		"tenant_id", o.facts.TenantID,
		"subject_id", o.facts.SubjectID,
		"instance_id", o.facts.InstanceID,
		"mode", o.facts.Mode,
	}
}

func boundedOutcome(outcome connectedsession.Outcome) string {
	switch outcome {
	case connectedsession.OutcomeNormal,
		connectedsession.OutcomeClientClosed,
		connectedsession.OutcomeIdleTimeout,
		connectedsession.OutcomeMaxDuration,
		connectedsession.OutcomeMessageTooBig,
		connectedsession.OutcomeInvalidTerminalFrame,
		connectedsession.OutcomeInvalidVNCFrame,
		connectedsession.OutcomeBackpressure,
		connectedsession.OutcomeRuntimeUnavailable,
		connectedsession.OutcomeRuntimeFailed,
		connectedsession.OutcomeTransportFailed,
		connectedsession.OutcomeShutdown,
		connectedsession.OutcomeInvalidStart:
		return string(outcome)
	default:
		return "invalid_start"
	}
}
