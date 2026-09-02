package websockettransport

import (
	"context"
	"errors"
	"io"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
)

func (h *Handler) bridgeSerial(ctx context.Context, connection *websocket.Conn, stream runtimeport.ByteStream) error {
	return h.bridgeByteStream(ctx, connection, stream, false)
}

func (h *Handler) bridgeVNC(ctx context.Context, connection *websocket.Conn, stream runtimeport.ByteStream) error {
	return h.bridgeByteStream(ctx, connection, stream, true)
}

func (h *Handler) bridgeByteStream(ctx context.Context, connection *websocket.Conn, stream runtimeport.ByteStream, binary bool) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	ioCtx, stopIO := context.WithCancel(context.WithoutCancel(ctx))
	defer stopIO()
	var lastActivity atomic.Int64
	lastActivity.Store(time.Now().UnixNano())
	touch := func() { lastActivity.Store(time.Now().UnixNano()) }
	fatal := make(chan error, 2)
	completed := make(chan error, 1)

	go func() {
		for {
			messageType, raw, err := connection.Read(ioCtx)
			if err != nil {
				fatal <- err
				return
			}
			touch()
			var payload []byte
			if binary {
				if messageType != websocket.MessageBinary {
					fatal <- errors.New("text frame rejected for VNC session")
					return
				}
				payload = raw
			} else {
				if messageType != websocket.MessageText {
					fatal <- errors.New("binary frame rejected for serial session")
					return
				}
				frame, decodeErr := decodeClientFrame(raw, h.config.MaxMessageBytes)
				if decodeErr != nil || frame.Type != "stdin" {
					fatal <- errors.New("invalid serial frame")
					return
				}
				payload = []byte(frame.Data)
			}
			if _, err := stream.Write(payload); err != nil {
				fatal <- err
				return
			}
			mode := "serial"
			if binary {
				mode = "vnc"
			}
			h.metrics.AddBytes(mode, "in", len(payload))
			touch()
		}
	}()

	go func() {
		buffer := make([]byte, 32*1024)
		for {
			n, err := stream.Read(buffer)
			if n > 0 {
				writeCtx, stop := context.WithTimeout(ioCtx, h.config.WriteTimeout)
				if binary {
					err = connection.Write(writeCtx, websocket.MessageBinary, append([]byte(nil), buffer[:n]...))
				} else {
					err = connection.Write(writeCtx, websocket.MessageText, encodeServerFrame(serverFrame{Type: "stdout", Data: string(buffer[:n])}))
				}
				stop()
				if err != nil {
					fatal <- err
					return
				}
				mode := "serial"
				if binary {
					mode = "vnc"
				}
				h.metrics.AddBytes(mode, "out", n)
				touch()
			}
			if err != nil {
				if ctx.Err() != nil || errors.Is(err, io.EOF) || errors.Is(err, io.ErrClosedPipe) || errors.Is(err, context.Canceled) {
					completed <- nil
				} else {
					completed <- err
				}
				return
			}
		}
	}()

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

	closeFailure := func() {
		if binary {
			_ = connection.Close(websocket.StatusInternalError, "console stream failed")
		} else {
			closeWithError(ctx, connection, "RUNTIME_STREAM_FAILED")
		}
	}
	for {
		select {
		case <-ctx.Done():
			if errors.Is(ctx.Err(), context.DeadlineExceeded) {
				_ = connection.Close(websocket.StatusPolicyViolation, "session expired")
			}
			return ctx.Err()
		case err := <-fatal:
			if status := websocket.CloseStatus(err); status == websocket.StatusNormalClosure || status == websocket.StatusGoingAway {
				return err
			}
			closeFailure()
			return err
		case err := <-completed:
			if err != nil {
				closeFailure()
				return err
			}
			_ = connection.Close(websocket.StatusNormalClosure, "")
			return nil
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
				return err
			}
		}
	}
}
