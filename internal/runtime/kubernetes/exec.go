package kubernetesruntime

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"sync"

	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"go.opentelemetry.io/otel"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/client-go/kubernetes/scheme"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	k8sexec "k8s.io/client-go/util/exec"
)

type executorFactory func(*rest.Config, string, *url.URL) (remotecommand.Executor, error)

type ExecAdapter struct {
	resolver    *PodResolver
	config      *rest.Config
	restClient  rest.Interface
	newExecutor executorFactory
}

func NewExecAdapter(config *rest.Config, client typedcorev1.CoreV1Interface) *ExecAdapter {
	return &ExecAdapter{resolver: NewPodResolver(client), config: rest.CopyConfig(config), restClient: client.RESTClient(), newExecutor: remotecommand.NewSPDYExecutor}
}

func (a *ExecAdapter) OpenExec(ctx context.Context, target runtimeport.ExecTarget, size session.TerminalSize) (runtimeport.ExecStream, error) {
	ctx, span := otel.Tracer("github.com/zhangzhe-ctrl/ani-session-gateway/runtime/kubernetes").Start(ctx, "runtime.kubernetes.open_exec")
	defer span.End()
	resolved, err := a.resolver.Resolve(ctx, target)
	if err != nil {
		return nil, err
	}
	command := append([]string(nil), target.Command...)
	if len(command) == 0 {
		command = []string{"/bin/sh"}
	}
	requestURL := a.restClient.Post().Namespace(resolved.Namespace).Resource("pods").Name(resolved.Name).SubResource("exec").VersionedParams(&corev1.PodExecOptions{Container: resolved.Container, Command: command, Stdin: true, Stdout: true, Stderr: !target.TTY, TTY: target.TTY}, scheme.ParameterCodec).URL()
	executor, err := a.newExecutor(a.config, http.MethodPost, requestURL)
	if err != nil {
		return nil, runtimeport.ErrUnavailable
	}
	return newExecStream(ctx, executor, target.TTY, size), nil
}

type execResult struct {
	code int
	err  error
}

type execStream struct {
	ctx           context.Context
	cancel        context.CancelFunc
	stdinR        *io.PipeReader
	stdinW        *io.PipeWriter
	stdoutR       *io.PipeReader
	stdoutW       *io.PipeWriter
	stderrR       *io.PipeReader
	stderrW       *io.PipeWriter
	resize        *sizeQueue
	done          chan struct{}
	result        execResult
	terminateOnce sync.Once
}

func newExecStream(parent context.Context, executor remotecommand.Executor, tty bool, initial session.TerminalSize) *execStream {
	ctx, cancel := context.WithCancel(parent)
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stderrR, stderrW := io.Pipe()
	stream := &execStream{ctx: ctx, cancel: cancel, stdinR: stdinR, stdinW: stdinW, stdoutR: stdoutR, stdoutW: stdoutW, stderrR: stderrR, stderrW: stderrW, resize: &sizeQueue{ctx: ctx, values: make(chan remotecommand.TerminalSize, 8)}, done: make(chan struct{})}
	if initial.Rows > 0 && initial.Cols > 0 {
		stream.resize.values <- remotecommand.TerminalSize{Height: initial.Rows, Width: initial.Cols}
	}
	go func() {
		options := remotecommand.StreamOptions{Stdin: stdinR, Stdout: stdoutW, Tty: tty}
		if tty {
			options.TerminalSizeQueue = stream.resize
		}
		if !tty {
			options.Stderr = stderrW
		}
		err := executor.StreamWithContext(ctx, options)
		result := execResult{err: err}
		var exitError k8sexec.ExitError
		if errors.As(err, &exitError) {
			result.code = exitError.ExitStatus()
			result.err = nil
		}
		stream.result = result
		stream.terminate()
		_ = stdoutW.Close()
		_ = stderrW.Close()
		close(stream.done)
	}()
	return stream
}

func (s *execStream) WriteStdin(data []byte) error        { _, err := s.stdinW.Write(data); return err }
func (s *execStream) ReadStdout(data []byte) (int, error) { return s.stdoutR.Read(data) }
func (s *execStream) ReadStderr(data []byte) (int, error) { return s.stderrR.Read(data) }
func (s *execStream) Resize(size session.TerminalSize) error {
	if size.Rows == 0 || size.Cols == 0 {
		return runtimeport.ErrInvalidTarget
	}
	select {
	case <-s.ctx.Done():
		return s.ctx.Err()
	case s.resize.values <- remotecommand.TerminalSize{Height: size.Rows, Width: size.Cols}:
		return nil
	default:
		return runtimeport.ErrBackpressure
	}
}
func (s *execStream) Wait() (int, error) { <-s.done; return s.result.code, s.result.err }
func (s *execStream) Close() error       { s.terminate(); return nil }
func (s *execStream) terminate() {
	s.terminateOnce.Do(func() { s.cancel(); _ = s.stdinW.Close(); _ = s.stdinR.Close() })
}

type sizeQueue struct {
	ctx    context.Context
	values chan remotecommand.TerminalSize
}

func (q *sizeQueue) Next() *remotecommand.TerminalSize {
	select {
	case <-q.ctx.Done():
		return nil
	case value := <-q.values:
		return &value
	}
}
