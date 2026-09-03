package connectedsession

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
)

const (
	terminalSubprotocol  = "ani.terminal.v1"
	vncSubprotocol       = "ani.vnc.v1"
	maxTerminalDimension = 4096
)

type Outcome string

const (
	OutcomeNormal               Outcome = "normal"
	OutcomeClientClosed         Outcome = "client_closed"
	OutcomeIdleTimeout          Outcome = "idle_timeout"
	OutcomeMaxDuration          Outcome = "max_duration"
	OutcomeMessageTooBig        Outcome = "message_too_big"
	OutcomeInvalidTerminalFrame Outcome = "invalid_terminal_frame"
	OutcomeInvalidVNCFrame      Outcome = "invalid_vnc_frame"
	OutcomeBackpressure         Outcome = "backpressure"
	OutcomeRuntimeUnavailable   Outcome = "runtime_unavailable"
	OutcomeRuntimeFailed        Outcome = "runtime_failed"
	OutcomeTransportFailed      Outcome = "transport_failed"
	OutcomeShutdown             Outcome = "shutdown"
	OutcomeInvalidStart         Outcome = "invalid_start"
)

type Accepted struct {
	Access session.ClaimedAccess
	Socket *websocket.Conn
}

type Dependencies struct {
	Manager  *session.Manager
	Exec     runtimeport.ExecRuntime
	VM       runtimeport.VMConsoleRuntime
	Clock    Clock
	Observer Observer
}

type Policy struct {
	MaxMessageBytes int64
	IdleTimeout     time.Duration
	WriteTimeout    time.Duration
	InboundQueue    int
	OutboundQueue   int
}

type SafeFacts struct {
	SessionID  string
	TenantID   string
	SubjectID  string
	InstanceID string
	Mode       session.Mode
}

type Finish struct {
	Outcome   Outcome
	Connected bool
	Duration  time.Duration
	BytesIn   uint64
	BytesOut  uint64
}

type Observer interface {
	Open(context.Context, SafeFacts) Observation
}

type Observation interface {
	Connected(time.Time)
	Finish(Finish)
}

type Module struct {
	manager  *session.Manager
	exec     runtimeport.ExecRuntime
	vm       runtimeport.VMConsoleRuntime
	clock    Clock
	observer Observer
	policy   Policy
}

func New(deps Dependencies, policy Policy) (*Module, error) {
	if deps.Manager == nil || deps.Exec == nil || deps.VM == nil || deps.Clock == nil || deps.Observer == nil {
		return nil, errors.New("Connected Session dependencies are required")
	}
	if policy.MaxMessageBytes <= 0 || policy.IdleTimeout <= 0 || policy.WriteTimeout <= 0 || policy.InboundQueue <= 0 || policy.OutboundQueue <= 0 {
		return nil, errors.New("positive Connected Session policy is required")
	}
	return &Module{manager: deps.Manager, exec: deps.Exec, vm: deps.VM, clock: deps.Clock, observer: deps.Observer, policy: policy}, nil
}

func (m *Module) Run(ctx context.Context, accepted Accepted) Outcome {
	observation := m.observer.Open(ctx, safeFacts(accepted.Access))
	if !validStart(accepted) {
		m.complete(ctx, accepted, observation, time.Time{}, OutcomeInvalidStart, 0, 0)
		return OutcomeInvalidStart
	}

	runCtx, cancel := context.WithCancel(ctx)
	if accepted.Access.Mode == session.ModeExec {
		stream, err := m.openExecStream(runCtx, accepted.Access)
		if err != nil {
			cancel()
			m.complete(ctx, accepted, observation, time.Time{}, OutcomeRuntimeUnavailable, 0, 0)
			return OutcomeRuntimeUnavailable
		}
		connectedAt := m.clock.Now()
		observation.Connected(connectedAt)
		outcome, bytesIn, bytesOut := m.carryExecStream(runCtx, cancel, accepted, stream)
		m.complete(ctx, accepted, observation, connectedAt, outcome, bytesIn, bytesOut)
		return outcome
	}
	stream, err := m.openByteStream(runCtx, accepted.Access)
	if err != nil {
		cancel()
		m.complete(ctx, accepted, observation, time.Time{}, OutcomeRuntimeUnavailable, 0, 0)
		return OutcomeRuntimeUnavailable
	}
	connectedAt := m.clock.Now()
	observation.Connected(connectedAt)
	outcome, bytesIn, bytesOut := m.carryByteStream(runCtx, cancel, accepted, stream)
	m.complete(ctx, accepted, observation, connectedAt, outcome, bytesIn, bytesOut)
	return outcome
}

func (m *Module) openByteStream(ctx context.Context, access session.ClaimedAccess) (runtimeport.ByteStream, error) {
	target := runtimeport.VMTarget{TenantID: access.Identity.TenantID, WorkloadName: access.Target.WorkloadName}
	switch access.Mode {
	case session.ModeSerial:
		return m.vm.OpenSerial(ctx, target)
	case session.ModeVNC:
		return m.vm.OpenVNC(ctx, target)
	default:
		return nil, runtimeport.ErrUnavailable
	}
}

func (m *Module) carryByteStream(ctx context.Context, cancel context.CancelFunc, accepted Accepted, stream runtimeport.ByteStream) (Outcome, uint64, uint64) {
	accepted.Socket.SetReadLimit(m.policy.MaxMessageBytes)
	outcomes := make(chan Outcome, 1)
	activity := make(chan struct{}, 1)
	var bytesIn atomic.Uint64
	var bytesOut atomic.Uint64
	var pumps sync.WaitGroup
	signalOutcome := func(outcome Outcome) {
		select {
		case outcomes <- outcome:
		default:
		}
	}
	touch := func() {
		select {
		case activity <- struct{}{}:
		default:
		}
	}

	pumps.Add(2)
	go func() {
		defer pumps.Done()
		for {
			messageType, raw, err := accepted.Socket.Read(ctx)
			if err != nil {
				signalOutcome(classifyTransportRead(ctx, err))
				return
			}
			payload, outcome := decodeBytePayload(accepted.Access.Mode, messageType, raw, m.policy.MaxMessageBytes)
			if outcome != "" {
				signalOutcome(outcome)
				return
			}
			if _, err := stream.Write(payload); err != nil {
				signalOutcome(classifyRuntimeError(err))
				return
			}
			bytesIn.Add(uint64(len(payload)))
			touch()
		}
	}()
	go func() {
		defer pumps.Done()
		buffer := make([]byte, 32*1024)
		for {
			n, err := stream.Read(buffer)
			if n > 0 {
				messageType, raw := encodeBytePayload(accepted.Access.Mode, buffer[:n])
				writeCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), m.policy.WriteTimeout)
				writeErr := accepted.Socket.Write(writeCtx, messageType, raw)
				stop()
				if writeErr != nil {
					signalOutcome(classifyTransportRead(ctx, writeErr))
					return
				}
				bytesOut.Add(uint64(n))
				touch()
			}
			if err != nil {
				if errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) {
					signalOutcome(OutcomeNormal)
				} else if ctx.Err() == nil {
					signalOutcome(OutcomeRuntimeFailed)
				}
				return
			}
		}
	}()

	idleTimer := m.clock.NewTimer(m.policy.IdleTimeout)
	defer idleTimer.Stop()
	maxAfter := accepted.Access.ExpiresAt.Sub(m.clock.Now())
	if maxAfter < 0 {
		maxAfter = 0
	}
	maxTimer := m.clock.NewTimer(maxAfter)
	defer maxTimer.Stop()

	var outcome Outcome
selectLoop:
	for {
		select {
		case outcome = <-outcomes:
			break selectLoop
		case <-activity:
			resetTimer(idleTimer, m.policy.IdleTimeout)
		case <-idleTimer.C():
			outcome = OutcomeIdleTimeout
			break selectLoop
		case <-maxTimer.C():
			outcome = OutcomeMaxDuration
			break selectLoop
		case <-ctx.Done():
			outcome = OutcomeShutdown
			break selectLoop
		}
	}

	// The selected outcome is immutable from this point onward. Cancellation
	// prevents either pump from starting another WebSocket payload operation.
	m.writeFinal(accepted.Socket, accepted.Access.Mode, outcome)
	cancel()
	grace := m.policy.WriteTimeout / 10
	if grace > 10*time.Millisecond {
		grace = 10 * time.Millisecond
	}
	if grace <= 0 {
		grace = time.Millisecond
	}
	if !waitPumps(&pumps, grace) {
		m.closeRuntime(stream, accepted.Access)
		waitPumps(&pumps, m.policy.WriteTimeout)
	} else {
		m.closeRuntime(stream, accepted.Access)
	}
	return outcome, bytesIn.Load(), bytesOut.Load()
}

func (m *Module) complete(ctx context.Context, accepted Accepted, observation Observation, connectedAt time.Time, outcome Outcome, bytesIn, bytesOut uint64) {
	if connectedAt.IsZero() {
		m.writeFinal(accepted.Socket, accepted.Access.Mode, outcome)
	}
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.policy.WriteTimeout)
	defer cancel()
	if err := m.manager.Close(closeCtx, accepted.Access.SessionID, accepted.Access.LeaseID, string(outcome)); err != nil {
		facts := safeFacts(accepted.Access)
		slog.Warn("session_lease_cleanup_failed", "session_id", facts.SessionID, "tenant_id", facts.TenantID, "subject_id", facts.SubjectID, "instance_id", facts.InstanceID, "mode", facts.Mode, "outcome", outcome)
	}
	finish := Finish{Outcome: outcome, Connected: !connectedAt.IsZero(), BytesIn: bytesIn, BytesOut: bytesOut}
	if finish.Connected {
		finish.Duration = m.clock.Now().Sub(connectedAt)
	}
	observation.Finish(finish)
}

func (m *Module) closeRuntime(stream io.Closer, access session.ClaimedAccess) {
	if err := stream.Close(); err != nil {
		facts := safeFacts(access)
		slog.Warn("session_runtime_cleanup_failed", "session_id", facts.SessionID, "tenant_id", facts.TenantID, "subject_id", facts.SubjectID, "instance_id", facts.InstanceID, "mode", facts.Mode)
	}
}

func (m *Module) writeFinal(conn *websocket.Conn, mode session.Mode, outcome Outcome) {
	if conn == nil || outcome == OutcomeClientClosed {
		return
	}
	status, reason := websocket.StatusInternalError, "stream failed"
	if outcome == OutcomeNormal {
		status, reason = websocket.StatusNormalClosure, ""
	} else if outcome == OutcomeIdleTimeout {
		status, reason = websocket.StatusPolicyViolation, "idle timeout"
	} else if outcome == OutcomeMaxDuration {
		status, reason = websocket.StatusPolicyViolation, "session expired"
	} else if outcome == OutcomeMessageTooBig {
		status, reason = websocket.StatusMessageTooBig, "message too big"
	} else if outcome == OutcomeShutdown {
		status, reason = websocket.StatusGoingAway, "server shutdown"
	} else if mode == session.ModeVNC {
		reason = "console stream failed"
	} else {
		code, message := terminalFailure(outcome)
		m.writeTerminalError(conn, code, message)
		reason = message
	}
	_ = conn.Close(status, reason)
}

func (m *Module) writeTerminalError(conn *websocket.Conn, code, message string) {
	writeCtx, cancel := context.WithTimeout(context.Background(), m.policy.WriteTimeout)
	defer cancel()
	raw, _ := json.Marshal(serverFrame{Type: "error", Code: code, Message: message})
	_ = conn.Write(writeCtx, websocket.MessageText, raw)
}

func validStart(accepted Accepted) bool {
	if accepted.Socket == nil || accepted.Access.SessionID == "" || accepted.Access.LeaseID == "" {
		return false
	}
	switch accepted.Access.Mode {
	case session.ModeExec:
		return accepted.Access.Exec != nil && accepted.Socket.Subprotocol() == terminalSubprotocol
	case session.ModeSerial:
		return accepted.Socket.Subprotocol() == terminalSubprotocol
	case session.ModeVNC:
		return accepted.Socket.Subprotocol() == vncSubprotocol
	default:
		return false
	}
}

func decodeBytePayload(mode session.Mode, messageType websocket.MessageType, raw []byte, maxBytes int64) ([]byte, Outcome) {
	if mode == session.ModeVNC {
		if messageType != websocket.MessageBinary {
			return nil, OutcomeInvalidVNCFrame
		}
		return raw, ""
	}
	if messageType != websocket.MessageText {
		return nil, OutcomeInvalidTerminalFrame
	}
	frame, err := decodeClientFrame(raw, maxBytes)
	if err != nil || frame.Type != "stdin" {
		return nil, OutcomeInvalidTerminalFrame
	}
	return []byte(frame.Data), ""
}

func encodeBytePayload(mode session.Mode, payload []byte) (websocket.MessageType, []byte) {
	if mode == session.ModeVNC {
		return websocket.MessageBinary, append([]byte(nil), payload...)
	}
	return websocket.MessageText, encodeServerFrame(serverFrame{Type: "stdout", Data: string(payload)})
}

type clientFrame struct {
	Type string `json:"type"`
	Data string `json:"data,omitempty"`
	Rows uint16 `json:"rows,omitempty"`
	Cols uint16 `json:"cols,omitempty"`
}

type serverFrame struct {
	Type    string `json:"type"`
	Data    string `json:"data,omitempty"`
	Code    any    `json:"code,omitempty"`
	Message string `json:"message,omitempty"`
}

func decodeClientFrame(raw []byte, maxStdin int64) (clientFrame, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	var frame clientFrame
	if err := decoder.Decode(&frame); err != nil {
		return clientFrame{}, err
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		if err == nil {
			return clientFrame{}, errors.New("multiple JSON values")
		}
		return clientFrame{}, err
	}
	switch frame.Type {
	case "stdin":
		if int64(len(frame.Data)) > maxStdin {
			return clientFrame{}, errors.New("stdin frame exceeds limit")
		}
	case "resize":
		if frame.Rows == 0 || frame.Cols == 0 || frame.Rows > maxTerminalDimension || frame.Cols > maxTerminalDimension {
			return clientFrame{}, errors.New("invalid terminal size")
		}
	default:
		return clientFrame{}, errors.New("unsupported frame type")
	}
	return frame, nil
}

func encodeServerFrame(frame serverFrame) []byte {
	encoded, _ := json.Marshal(frame)
	return encoded
}

func classifyTransportRead(ctx context.Context, err error) Outcome {
	if ctx.Err() != nil {
		return OutcomeShutdown
	}
	switch websocket.CloseStatus(err) {
	case websocket.StatusNormalClosure, websocket.StatusGoingAway:
		return OutcomeClientClosed
	case websocket.StatusMessageTooBig:
		return OutcomeMessageTooBig
	default:
		return OutcomeTransportFailed
	}
}

func classifyRuntimeError(err error) Outcome {
	if errors.Is(err, runtimeport.ErrBackpressure) {
		return OutcomeBackpressure
	}
	return OutcomeRuntimeFailed
}

func terminalFailure(outcome Outcome) (string, string) {
	switch outcome {
	case OutcomeInvalidTerminalFrame:
		return "INVALID_TERMINAL_FRAME", "invalid terminal frame"
	case OutcomeBackpressure:
		return "BACKPRESSURE_LIMIT", "terminal stream failed"
	case OutcomeRuntimeUnavailable:
		return "RUNTIME_UNAVAILABLE", "terminal stream failed"
	default:
		return "RUNTIME_STREAM_FAILED", "terminal stream failed"
	}
}

func safeFacts(access session.ClaimedAccess) SafeFacts {
	return SafeFacts{SessionID: access.SessionID, TenantID: access.Identity.TenantID, SubjectID: access.Identity.SubjectID, InstanceID: access.Identity.InstanceID, Mode: access.Mode}
}

func resetTimer(timer Timer, after time.Duration) {
	if !timer.Stop() {
		select {
		case <-timer.C():
		default:
		}
	}
	timer.Reset(after)
}

func waitPumps(pumps *sync.WaitGroup, timeout time.Duration) bool {
	done := make(chan struct{})
	go func() {
		pumps.Wait()
		close(done)
	}()
	timer := time.NewTimer(timeout)
	defer timer.Stop()
	select {
	case <-done:
		return true
	case <-timer.C:
		return false
	}
}
