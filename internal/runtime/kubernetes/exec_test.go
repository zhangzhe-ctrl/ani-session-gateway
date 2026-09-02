package kubernetesruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
	"time"

	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

type executorFunc func(context.Context, remotecommand.StreamOptions) error

func (f executorFunc) Stream(options remotecommand.StreamOptions) error {
	return f(context.Background(), options)
}
func (f executorFunc) StreamWithContext(ctx context.Context, options remotecommand.StreamOptions) error {
	return f(ctx, options)
}

type exitError struct{ code int }

func (e exitError) Error() string   { return "remote command exited" }
func (e exitError) String() string  { return e.Error() }
func (e exitError) Exited() bool    { return true }
func (e exitError) ExitStatus() int { return e.code }

func TestExecStreamStdinStdoutStderrAndExit(t *testing.T) {
	executor := executorFunc(func(_ context.Context, options remotecommand.StreamOptions) error {
		input := make([]byte, 4)
		if _, err := io.ReadFull(options.Stdin, input); err != nil {
			return err
		}
		_, _ = options.Stdout.Write(append([]byte("out:"), input...))
		_, _ = options.Stderr.Write([]byte("err"))
		return exitError{code: 7}
	})
	stream := newExecStream(context.Background(), executor, false, session.TerminalSize{})
	stdoutResult := make(chan []byte, 1)
	stderrResult := make(chan []byte, 1)
	go func() { data, _ := io.ReadAll(readerFunc(stream.ReadStdout)); stdoutResult <- data }()
	go func() { data, _ := io.ReadAll(readerFunc(stream.ReadStderr)); stderrResult <- data }()
	if err := stream.WriteStdin([]byte("ping")); err != nil {
		t.Fatal(err)
	}
	code, err := stream.Wait()
	if err != nil || code != 7 {
		t.Fatalf("wait code=%d err=%v", code, err)
	}
	stdout, stderr := <-stdoutResult, <-stderrResult
	if string(stdout) != "out:ping" || string(stderr) != "err" {
		t.Fatalf("stdout=%q stderr=%q", stdout, stderr)
	}
}

func TestExecStreamResizeBackpressureAndCancellation(t *testing.T) {
	sizes := make(chan remotecommand.TerminalSize, 2)
	executor := executorFunc(func(ctx context.Context, options remotecommand.StreamOptions) error {
		for {
			size := options.TerminalSizeQueue.Next()
			if size == nil {
				return ctx.Err()
			}
			sizes <- *size
		}
	})
	stream := newExecStream(context.Background(), executor, true, session.TerminalSize{Rows: 24, Cols: 80})
	select {
	case size := <-sizes:
		if size.Height != 24 || size.Width != 80 {
			t.Fatalf("initial size=%#v", size)
		}
	case <-time.After(time.Second):
		t.Fatal("initial resize not observed")
	}
	if err := stream.Resize(session.TerminalSize{Rows: 30, Cols: 120}); err != nil {
		t.Fatal(err)
	}
	select {
	case size := <-sizes:
		if size.Height != 30 || size.Width != 120 {
			t.Fatalf("resize=%#v", size)
		}
	case <-time.After(time.Second):
		t.Fatal("resize not observed")
	}
	if err := stream.Resize(session.TerminalSize{}); err == nil {
		t.Fatal("zero resize accepted")
	}
	if err := stream.Close(); err != nil {
		t.Fatal(err)
	}
	_, err := stream.Wait()
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("cancel error=%v", err)
	}
}

func TestExecAdapterResolvesPodAndBuildsRemoteCommandRequest(t *testing.T) {
	apiServer := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if !strings.Contains(request.URL.Query().Get("labelSelector"), tenantLabel+"=tenant-a") || !strings.Contains(request.URL.Query().Get("labelSelector"), instanceLabel+"=workload-a") {
			t.Errorf("selector=%q", request.URL.Query().Get("labelSelector"))
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []corev1.Pod{*pod("ani-tenant-tenant-a", "pod-a", "tenant-a", "workload-a", time.Now(), true, "app")}})
	}))
	defer apiServer.Close()
	config := &rest.Config{Host: apiServer.URL}
	client, err := typedcorev1.NewForConfig(config)
	if err != nil {
		t.Fatal(err)
	}
	adapter := NewExecAdapter(config, client)
	var captured *url.URL
	adapter.newExecutor = func(_ *rest.Config, method string, requestURL *url.URL) (remotecommand.Executor, error) {
		if method != http.MethodPost {
			t.Fatalf("method=%s", method)
		}
		captured = requestURL
		return executorFunc(func(_ context.Context, options remotecommand.StreamOptions) error {
			_, _ = options.Stdout.Write([]byte("ani-terminal-ok"))
			return nil
		}), nil
	}
	var runtime runtimeport.ExecRuntime = adapter
	stream, err := runtime.OpenExec(context.Background(), runtimeport.ExecTarget{TenantID: "tenant-a", WorkloadName: "workload-a", WorkloadKind: session.WorkloadContainer, Command: []string{"/bin/sh"}, TTY: true}, session.TerminalSize{Rows: 30, Cols: 120})
	if err != nil {
		t.Fatal(err)
	}
	stdout, err := io.ReadAll(readerFunc(stream.ReadStdout))
	if err != nil {
		t.Fatal(err)
	}
	if code, err := stream.Wait(); err != nil || code != 0 {
		t.Fatalf("wait code=%d err=%v", code, err)
	}
	if string(stdout) != "ani-terminal-ok" {
		t.Fatalf("stdout=%q", stdout)
	}
	if captured == nil || captured.Path != "/api/v1/namespaces/ani-tenant-tenant-a/pods/pod-a/exec" || captured.Query().Get("container") != "app" || captured.Query().Get("command") != "/bin/sh" || captured.Query().Get("tty") != "true" {
		t.Fatalf("exec URL=%v", captured)
	}
}

type readerFunc func([]byte) (int, error)

func (f readerFunc) Read(data []byte) (int, error) { return f(data) }
