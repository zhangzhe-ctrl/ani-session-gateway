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
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/websocket/connectedsession"
)

const (
	TerminalSubprotocol = "ani.terminal.v1"
	VNCSubprotocol      = "ani.vnc.v1"
)

type Config struct {
	AllowedOrigins map[string]struct{}
	CleanupTimeout time.Duration
}

type Handler struct {
	manager   *session.Manager
	connected *connectedsession.Module
	config    Config
}

func NewHandler(manager *session.Manager, connected *connectedsession.Module, config Config) (*Handler, error) {
	if manager == nil || connected == nil || len(config.AllowedOrigins) == 0 || config.CleanupTimeout <= 0 {
		return nil, errors.New("manager, Connected Session module, origins and cleanup timeout are required")
	}
	return &Handler{manager: manager, connected: connected, config: config}, nil
}

func RegisterRoutes(router chi.Router, handler *Handler) {
	router.Get("/api/v1/realtime/sessions/{sessionID}", handler.ServeHTTP)
}

func (h *Handler) ServeHTTP(w http.ResponseWriter, request *http.Request) {
	ctx, span := observability.StartSpan(request.Context(), "websocket.admission")
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
	access, err := h.manager.Claim(request.Context(), chi.URLParam(request, "sessionID"), ticket)
	if err != nil {
		writeClaimError(w, err)
		return
	}
	release := true
	reason := "upgrade_failed"
	defer func() {
		if !release {
			return
		}
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(request.Context()), h.config.CleanupTimeout)
		defer cancel()
		if err := h.manager.Close(closeCtx, access.SessionID, access.LeaseID, reason); err != nil {
			slog.Warn("session_admission_cleanup_failed", "session_id", access.SessionID, "tenant_id", access.Identity.TenantID, "subject_id", access.Identity.SubjectID, "instance_id", access.Identity.InstanceID, "mode", access.Mode, "reason", reason)
		}
	}()

	expected := TerminalSubprotocol
	if access.Mode == session.ModeVNC {
		expected = VNCSubprotocol
	}
	if !offersSubprotocol(request.Header.Get("Sec-WebSocket-Protocol"), expected) {
		reason = "subprotocol_rejected"
		http.Error(w, "session subprotocol does not match mode", http.StatusBadRequest)
		return
	}
	connection, err := websocket.Accept(w, request, &websocket.AcceptOptions{
		Subprotocols:       []string{expected},
		InsecureSkipVerify: true,
		CompressionMode:    websocket.CompressionDisabled,
	})
	if err != nil {
		return
	}
	release = false
	h.connected.Run(request.Context(), connectedsession.Accepted{Access: access, Socket: connection})
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
