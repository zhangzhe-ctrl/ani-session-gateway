package connectedsession

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
)

type outboundFrame struct {
	frame serverFrame
	ack   chan error
}

type execResult struct {
	code int
	err  error
}

func (m *Module) openExecStream(ctx context.Context, access session.ClaimedAccess) (runtimeport.ExecStream, error) {
	if access.Exec == nil {
		return nil, runtimeport.ErrInvalidTarget
	}
	target := runtimeport.ExecTarget{
		TenantID:     access.Identity.TenantID,
		WorkloadName: access.Target.WorkloadName,
		WorkloadKind: access.Target.WorkloadKind,
		Container:    access.Exec.Container,
		Command:      append([]string(nil), access.Exec.Command...),
		TTY:          access.Exec.TTY,
	}
	return m.exec.OpenExec(ctx, target, access.Exec.Size)
}

func (m *Module) carryExecStream(ctx context.Context, cancel context.CancelFunc, accepted Accepted, stream runtimeport.ExecStream) (Outcome, uint64, uint64) {
	accepted.Socket.SetReadLimit(m.policy.MaxMessageBytes)
	outcomes := make(chan Outcome, 1)
	activity := make(chan struct{}, 1)
	completed := make(chan execResult, 1)
	inputs := make(chan clientFrame, m.policy.InboundQueue)
	outbound := make(chan outboundFrame, m.policy.OutboundQueue)
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
	enqueue := func(item outboundFrame) bool {
		select {
		case outbound <- item:
			return true
		case <-ctx.Done():
			return false
		default:
			signalOutcome(OutcomeBackpressure)
			return false
		}
	}

	pumps.Add(1)
	go func() {
		defer pumps.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case item := <-outbound:
				writeCtx, stop := context.WithTimeout(context.WithoutCancel(ctx), m.policy.WriteTimeout)
				err := accepted.Socket.Write(writeCtx, websocket.MessageText, encodeServerFrame(item.frame))
				stop()
				if item.ack != nil {
					item.ack <- err
				}
				if err != nil {
					signalOutcome(classifyTransportRead(ctx, err))
					return
				}
				if item.frame.Type == "stdout" || item.frame.Type == "stderr" {
					bytesOut.Add(uint64(len(item.frame.Data)))
				}
				touch()
			}
		}
	}()

	pumps.Add(1)
	go func() {
		defer pumps.Done()
		for {
			messageType, raw, err := accepted.Socket.Read(ctx)
			if err != nil {
				signalOutcome(classifyTransportRead(ctx, err))
				return
			}
			if messageType != websocket.MessageText {
				signalOutcome(OutcomeInvalidTerminalFrame)
				return
			}
			frame, err := decodeClientFrame(raw, m.policy.MaxMessageBytes)
			if err != nil {
				signalOutcome(OutcomeInvalidTerminalFrame)
				return
			}
			select {
			case inputs <- frame:
				touch()
			case <-ctx.Done():
				return
			default:
				signalOutcome(OutcomeBackpressure)
				return
			}
		}
	}()

	pumps.Add(1)
	go func() {
		defer pumps.Done()
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-inputs:
				var err error
				if frame.Type == "stdin" {
					err = stream.WriteStdin([]byte(frame.Data))
					if err == nil {
						bytesIn.Add(uint64(len(frame.Data)))
					}
				} else {
					err = stream.Resize(session.TerminalSize{Rows: frame.Rows, Cols: frame.Cols})
				}
				if err != nil {
					signalOutcome(classifyRuntimeError(err))
					return
				}
				touch()
			}
		}
	}()

	var outputs sync.WaitGroup
	readOutput := func(kind string, read func([]byte) (int, error)) {
		defer outputs.Done()
		buffer := make([]byte, 32*1024)
		for {
			n, err := read(buffer)
			if n > 0 && !enqueue(outboundFrame{frame: serverFrame{Type: kind, Data: string(buffer[:n])}}) {
				return
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) && !errors.Is(err, io.ErrClosedPipe) && ctx.Err() == nil {
					signalOutcome(OutcomeRuntimeFailed)
				}
				return
			}
		}
	}
	outputs.Add(1)
	pumps.Add(1)
	go func() { defer pumps.Done(); readOutput("stdout", stream.ReadStdout) }()
	if accepted.Access.Exec != nil && !accepted.Access.Exec.TTY {
		outputs.Add(1)
		pumps.Add(1)
		go func() { defer pumps.Done(); readOutput("stderr", stream.ReadStderr) }()
	}
	pumps.Add(1)
	go func() {
		defer pumps.Done()
		code, err := stream.Wait()
		outputs.Wait()
		select {
		case completed <- execResult{code: code, err: err}:
		case <-ctx.Done():
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
		case result := <-completed:
			if result.err != nil {
				outcome = OutcomeRuntimeFailed
				break selectLoop
			}
			ack := make(chan error, 1)
			if !enqueue(outboundFrame{frame: serverFrame{Type: "exit", Code: result.code}, ack: ack}) {
				outcome = OutcomeBackpressure
				break selectLoop
			}
			select {
			case err := <-ack:
				if err == nil {
					outcome = OutcomeNormal
				} else {
					outcome = classifyTransportRead(ctx, err)
				}
			case outcome = <-outcomes:
			case <-ctx.Done():
				outcome = OutcomeShutdown
			}
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
