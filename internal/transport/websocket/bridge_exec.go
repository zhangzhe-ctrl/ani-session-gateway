package websockettransport

import (
	"context"
	"errors"
	"io"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
)

type streamResult struct {
	code int
	err  error
}
type outboundFrame struct {
	frame serverFrame
	ack   chan error
}

func (h *Handler) bridgeExec(ctx context.Context, connection *websocket.Conn, stream runtimeport.ExecStream, tty bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ioCtx, stopIO := context.WithCancel(context.WithoutCancel(ctx))
	defer stopIO()
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	touch := func() { lastActivity.Store(time.Now().UnixNano()) }
	fatal := make(chan error, 1)
	fail := func(err error) {
		select {
		case fatal <- err:
		default:
		}
	}
	outbound := make(chan outboundFrame, h.config.OutboundQueue)
	enqueue := func(frame outboundFrame) error {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case outbound <- frame:
			return nil
		default:
			return runtimeport.ErrBackpressure
		}
	}
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case item := <-outbound:
				writeCtx, stop := context.WithTimeout(ioCtx, h.config.WriteTimeout)
				err := connection.Write(writeCtx, websocket.MessageText, encodeServerFrame(item.frame))
				stop()
				if item.ack != nil {
					item.ack <- err
				}
				if err != nil {
					fail(err)
					return
				}
				if item.frame.Type == "stdout" || item.frame.Type == "stderr" {
					h.metrics.AddBytes("exec", "out", len(item.frame.Data))
				}
				touch()
			}
		}
	}()

	inputs := make(chan clientFrame, h.config.InboundQueue)
	go func() {
		for {
			messageType, raw, err := connection.Read(ioCtx)
			if err != nil {
				fail(err)
				return
			}
			touch()
			if messageType != websocket.MessageText {
				fail(errors.New("binary frame rejected for terminal session"))
				return
			}
			frame, err := decodeClientFrame(raw, h.config.MaxMessageBytes)
			if err != nil {
				fail(err)
				return
			}
			select {
			case inputs <- frame:
			case <-ctx.Done():
				return
			default:
				fail(runtimeport.ErrBackpressure)
				return
			}
		}
	}()
	go func() {
		for {
			select {
			case <-ctx.Done():
				return
			case frame := <-inputs:
				var err error
				if frame.Type == "stdin" {
					err = stream.WriteStdin([]byte(frame.Data))
				} else {
					err = stream.Resize(terminalSize(frame))
				}
				if err != nil {
					fail(err)
					return
				}
				if frame.Type == "stdin" {
					h.metrics.AddBytes("exec", "in", len(frame.Data))
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
			if n > 0 {
				if sendErr := enqueue(outboundFrame{frame: serverFrame{Type: kind, Data: string(buffer[:n])}}); sendErr != nil {
					fail(sendErr)
					return
				}
			}
			if err != nil {
				if !errors.Is(err, io.EOF) && !errors.Is(err, context.Canceled) {
					fail(err)
				}
				return
			}
		}
	}
	outputs.Add(1)
	go readOutput("stdout", stream.ReadStdout)
	if !tty {
		outputs.Add(1)
		go readOutput("stderr", stream.ReadStderr)
	}
	completed := make(chan streamResult, 1)
	go func() { code, err := stream.Wait(); outputs.Wait(); completed <- streamResult{code: code, err: err} }()

	idleTick := h.config.IdleTimeout / 4
	if idleTick > time.Second {
		idleTick = time.Second
	}
	if idleTick < 10*time.Millisecond {
		idleTick = 10 * time.Millisecond
	}
	idleTicker := time.NewTicker(idleTick)
	defer idleTicker.Stop()
	pingInterval := h.config.IdleTimeout / 2
	if pingInterval > 30*time.Second {
		pingInterval = 30 * time.Second
	}
	if pingInterval < 25*time.Millisecond {
		pingInterval = 25 * time.Millisecond
	}
	pingTicker := time.NewTicker(pingInterval)
	defer pingTicker.Stop()
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				_ = connection.Close(websocket.StatusPolicyViolation, "session expired")
			}
			return ctx.Err()
		case err := <-fatal:
			if websocket.CloseStatus(err) == websocket.StatusNormalClosure || websocket.CloseStatus(err) == websocket.StatusGoingAway {
				return err
			}
			closeWithError(ctx, connection, errorCode(err))
			return err
		case result := <-completed:
			if result.err != nil {
				closeWithError(ctx, connection, "RUNTIME_STREAM_FAILED")
				return result.err
			}
			ack := make(chan error, 1)
			if err := enqueue(outboundFrame{frame: serverFrame{Type: "exit", Code: result.code}, ack: ack}); err != nil {
				return err
			}
			select {
			case err := <-ack:
				return err
			case <-ctx.Done():
				return ctx.Err()
			}
		case <-idleTicker.C:
			if time.Since(time.Unix(0, lastActivity.Load())) >= h.config.IdleTimeout {
				_ = connection.Close(websocket.StatusPolicyViolation, "idle timeout")
				return context.DeadlineExceeded
			}
		case <-pingTicker.C:
			pingCtx, stop := context.WithTimeout(ioCtx, h.config.WriteTimeout)
			err := connection.Ping(pingCtx)
			stop()
			if err != nil {
				fail(err)
			}
		}
	}
}

func errorCode(err error) string {
	if errors.Is(err, runtimeport.ErrBackpressure) {
		return "BACKPRESSURE_LIMIT"
	}
	return "INVALID_TERMINAL_FRAME"
}
