package websockettransport

import (
	"context"
	"errors"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/go-chi/chi/v5"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/observability"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
)

type Config struct {
	AllowedOrigins  map[string]struct{}
	MaxMessageBytes int64
	IdleTimeout     time.Duration
	WriteTimeout    time.Duration
	OutboundQueue   int
	InboundQueue    int
}

type Handler struct {
	manager *session.Manager
	exec    runtimeport.ExecRuntime
	vm      runtimeport.VMConsoleRuntime
	config  Config
	metrics *observability.SessionMetrics
}

func (h *Handler) SetMetrics(metrics *observability.SessionMetrics) { h.metrics = metrics }

func NewHandler(manager *session.Manager, exec runtimeport.ExecRuntime, vm runtimeport.VMConsoleRuntime, config Config) (*Handler, error) {
	if manager == nil || exec == nil || len(config.AllowedOrigins) == 0 || config.MaxMessageBytes <= 0 || config.IdleTimeout <= 0 {
		return nil, errors.New("manager, exec runtime, origins, message limit and idle timeout are required")
	}
	if config.WriteTimeout <= 0 {
		config.WriteTimeout = 10 * time.Second
	}
	if config.OutboundQueue <= 0 {
		config.OutboundQueue = 32
	}
	if config.InboundQueue <= 0 {
		config.InboundQueue = 32
	}
	return &Handler{manager: manager, exec: exec, vm: vm, config: config}, nil
}

func RegisterRoutes(router chi.Router, handler *Handler) {
	router.Get("/api/v1/realtime/sessions/{sessionID}", handler.ServeHTTP)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	ctx, span := observability.StartSpan(request.Context(), "websocket.session")
	defer span.End()
	request = request.WithContext(ctx)
	if !h.originAllowed(request.Header.Get("Origin")) {
		http.Error(w, "origin is not allowed", http.StatusForbidden)
		return
	}
	if !offersKnownSubprotocol(request.Header.Get("Sec-WebSocket-Protocol")) {
		http.Error(w, "supported WebSocket subprotocol is required", http.StatusBadRequest)
		return
	}
	ticket := request.URL.Query().Get("ticket")
	if ticket == "" {
		http.Error(w, "session ticket is required", http.StatusUnauthorized)
		return
	}
	lease, err := h.manager.Claim(request.Context(), chi.URLParam(request, "sessionID"), ticket)
	if err != nil {
		writeClaimError(w, err)
		return
	}
	reason := "transport_closed"
	connectedAt := time.Time{}
	defer func() {
		_ = h.manager.Close(context.Background(), lease.Session.ID, lease.ID, reason)
		if connectedAt.IsZero() {
			slog.Warn("session_failed", "session_id", lease.Session.ID, "tenant_id", lease.Session.TenantID, "subject_id", lease.Session.SubjectID, "instance_id", lease.Session.InstanceID, "mode", lease.Session.Mode, "reason", reason)
			return
		}
		duration := time.Since(connectedAt)
		h.metrics.Closed(string(lease.Session.Mode), duration)
		slog.Info("session_closed", "session_id", lease.Session.ID, "tenant_id", lease.Session.TenantID, "subject_id", lease.Session.SubjectID, "instance_id", lease.Session.InstanceID, "mode", lease.Session.Mode, "duration_ms", duration.Milliseconds(), "close_reason", reason)
	}()
	markConnected := func() {
		connectedAt = time.Now()
		h.metrics.Connected(string(lease.Session.Mode))
		slog.Info("session_connected", "session_id", lease.Session.ID, "tenant_id", lease.Session.TenantID, "subject_id", lease.Session.SubjectID, "instance_id", lease.Session.InstanceID, "mode", lease.Session.Mode)
	}
	expected := TerminalSubprotocol
	if lease.Session.Mode == session.ModeVNC {
		expected = VNCSubprotocol
	}
	if !offersSubprotocol(request.Header.Get("Sec-WebSocket-Protocol"), expected) {
		reason = "subprotocol_rejected"
		http.Error(w, "session subprotocol does not match mode", http.StatusBadRequest)
		return
	}
	connection, err := websocket.Accept(w, request, &websocket.AcceptOptions{Subprotocols: []string{expected}, InsecureSkipVerify: true, CompressionMode: websocket.CompressionDisabled})
	if err != nil {
		reason = "upgrade_failed"
		return
	}
	connection.SetReadLimit(h.config.MaxMessageBytes)
	defer connection.Close(websocket.StatusNormalClosure, "")
	deadline := lease.ExpiresAt
	streamCtx, cancel := context.WithDeadline(request.Context(), deadline)
	defer cancel()
	switch lease.Session.Mode {
	case session.ModeExec:
		stream, err := h.exec.OpenExec(streamCtx, runtimeport.ExecTarget{TenantID: lease.Session.TenantID, WorkloadName: lease.Session.WorkloadName, WorkloadKind: lease.Session.WorkloadKind, Container: lease.Session.Container, Command: lease.Session.Command, TTY: lease.Session.TTY}, session.TerminalSize{Rows: lease.Session.Rows, Cols: lease.Session.Cols})
		if err != nil {
			reason = "runtime_open_failed"
			h.metrics.RuntimeError(string(lease.Session.Mode), "open_failed")
			closeWithError(streamCtx, connection, "RUNTIME_UNAVAILABLE")
			return
		}
		defer stream.Close()
		markConnected()
		if err := h.bridgeExec(streamCtx, connection, stream, lease.Session.TTY); err != nil {
			reason = closeReason(err)
			h.metrics.RuntimeError(string(lease.Session.Mode), runtimeErrorCode(err))
			return
		}
		reason = "normal"
	case session.ModeSerial:
		if h.vm == nil {
			reason = "runtime_not_configured"
			closeWithError(streamCtx, connection, "RUNTIME_UNAVAILABLE")
			return
		}
		stream, err := h.vm.OpenSerial(streamCtx, runtimeport.VMTarget{TenantID: lease.Session.TenantID, WorkloadName: lease.Session.WorkloadName})
		if err != nil {
			reason = "runtime_open_failed"
			h.metrics.RuntimeError(string(lease.Session.Mode), "open_failed")
			closeWithError(streamCtx, connection, "RUNTIME_UNAVAILABLE")
			return
		}
		defer stream.Close()
		markConnected()
		if err := h.bridgeSerial(streamCtx, connection, stream); err != nil {
			reason = closeReason(err)
			h.metrics.RuntimeError(string(lease.Session.Mode), runtimeErrorCode(err))
			return
		}
		reason = "normal"
	case session.ModeVNC:
		if h.vm == nil {
			reason = "runtime_not_configured"
			_ = connection.Close(websocket.StatusInternalError, "console runtime unavailable")
			return
		}
		stream, err := h.vm.OpenVNC(streamCtx, runtimeport.VMTarget{TenantID: lease.Session.TenantID, WorkloadName: lease.Session.WorkloadName})
		if err != nil {
			reason = "runtime_open_failed"
			h.metrics.RuntimeError(string(lease.Session.Mode), "open_failed")
			_ = connection.Close(websocket.StatusInternalError, "console runtime unavailable")
			return
		}
		defer stream.Close()
		markConnected()
		if err := h.bridgeVNC(streamCtx, connection, stream); err != nil {
			reason = closeReason(err)
			h.metrics.RuntimeError(string(lease.Session.Mode), runtimeErrorCode(err))
			return
		}
		reason = "normal"
	default:
		reason = "runtime_not_configured"
		closeWithError(streamCtx, connection, "RUNTIME_UNAVAILABLE")
	}
}

func (h *Handler) originAllowed(origin string) bool {
	_, ok := h.config.AllowedOrigins[origin]
	return ok && origin != ""
}
func offersKnownSubprotocol(raw string) bool {
	return offersSubprotocol(raw, TerminalSubprotocol) || offersSubprotocol(raw, VNCSubprotocol)
}
func offersSubprotocol(raw, expected string) bool {
	for _, candidate := range strings.Split(raw, ",") {
		if strings.TrimSpace(candidate) == expected {
			return true
		}
	}
	return false
}

func writeClaimError(w http.ResponseWriter, err error) {
	switch {
	case errors.Is(err, session.ErrNotFound):
		http.Error(w, "session not found", http.StatusNotFound)
	case errors.Is(err, session.ErrInvalidTicket):
		http.Error(w, "session ticket rejected", http.StatusForbidden)
	case errors.Is(err, session.ErrCapacity):
		http.Error(w, "session capacity exhausted", http.StatusTooManyRequests)
	case errors.Is(err, session.ErrUnavailable):
		http.Error(w, "session store unavailable", http.StatusServiceUnavailable)
	default:
		http.Error(w, "session is no longer claimable", http.StatusUnprocessableEntity)
	}
}

func closeWithError(ctx context.Context, connection *websocket.Conn, code string) {
	writeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 2*time.Second)
	defer cancel()
	_ = connection.Write(writeCtx, websocket.MessageText, encodeServerFrame(serverFrame{Type: "error", Code: code, Message: "terminal stream failed"}))
	_ = connection.Close(websocket.StatusInternalError, "terminal stream failed")
}
func closeReason(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, runtimeport.ErrBackpressure) {
		return "backpressure"
	}
	if websocket.CloseStatus(err) == websocket.StatusNormalClosure {
		return "client_closed"
	}
	return "transport_error"
}

func runtimeErrorCode(err error) string {
	if errors.Is(err, context.DeadlineExceeded) {
		return "timeout"
	}
	if errors.Is(err, runtimeport.ErrBackpressure) {
		return "backpressure"
	}
	if status := websocket.CloseStatus(err); status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
		return "client_closed"
	}
	return "stream_failed"
}
