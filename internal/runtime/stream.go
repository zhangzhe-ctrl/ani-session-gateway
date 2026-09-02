package runtime

import (
	"context"
	"errors"
	"io"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
)

var (
	ErrInvalidTarget      = errors.New("invalid runtime target")
	ErrTargetNotFound     = errors.New("runtime target not found")
	ErrTargetNotReady     = errors.New("runtime target not ready")
	ErrAmbiguousContainer = errors.New("runtime container is ambiguous")
	ErrBackpressure       = errors.New("runtime backpressure limit exceeded")
	ErrUnavailable        = errors.New("runtime unavailable")
)

type ExecTarget struct {
	TenantID     string
	WorkloadName string
	WorkloadKind session.WorkloadKind
	Container    string
	Command      []string
	TTY          bool
}

type VMTarget struct{ TenantID, WorkloadName string }

type ExecRuntime interface {
	OpenExec(context.Context, ExecTarget, session.TerminalSize) (ExecStream, error)
}

type VMConsoleRuntime interface {
	OpenSerial(context.Context, VMTarget) (ByteStream, error)
	OpenVNC(context.Context, VMTarget) (ByteStream, error)
}

type ExecStream interface {
	WriteStdin([]byte) error
	ReadStdout([]byte) (int, error)
	ReadStderr([]byte) (int, error)
	Resize(session.TerminalSize) error
	Wait() (int, error)
	Close() error
}

type ByteStream interface {
	io.Reader
	io.Writer
	io.Closer
}
