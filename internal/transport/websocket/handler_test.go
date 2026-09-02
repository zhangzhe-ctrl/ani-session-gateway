package websockettransport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/observability"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/memory"
	httptransport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/http"
)

const testOrigin = "http://console.example.test"

func TestExecFramesResizeExitAndTicketReplay(t *testing.T) {
	runtime := &fakeExecRuntime{completeOnInput: true}
	manager, issued := testManagerAndSession(t, 5*time.Second)
	handler := testHandler(t, manager, runtime, 1024, time.Second, 8, 8)
	server := testServer(t, handler)
	connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{TerminalSubprotocol})
	stream := runtime.waitStream(t)
	writeJSON(t, connection, clientFrame{Type: "resize", Rows: 30, Cols: 120})
	select {
	case size := <-stream.resizes:
		if size.Rows != 30 || size.Cols != 120 {
			t.Fatalf("resize=%#v", size)
		}
	case <-time.After(time.Second):
		t.Fatal("resize not forwarded")
	}
	writeJSON(t, connection, clientFrame{Type: "stdin", Data: "printf ani-terminal-ok"})
	frames := readServerFrames(t, connection, 2)
	if frames[0].Type != "stdout" || frames[0].Data != "printf ani-terminal-ok" || frames[1].Type != "exit" {
		t.Fatalf("unexpected frames: %#v", frames)
	}
	if connection.Subprotocol() != TerminalSubprotocol {
		t.Fatalf("subprotocol=%q", connection.Subprotocol())
	}
	_, response, err := websocket.Dial(context.Background(), sessionURL(server.URL, issued), dialOptions(testOrigin, []string{TerminalSubprotocol}))
	if err == nil || response == nil || response.StatusCode != http.StatusUnprocessableEntity {
		t.Fatalf("ticket replay response=%v err=%v", response, err)
	}
}

func TestOriginAndSubprotocolRejectBeforeClaim(t *testing.T) {
	runtime := &fakeExecRuntime{}
	manager, issued := testManagerAndSession(t, 5*time.Second)
	server := testServer(t, testHandler(t, manager, runtime, 1024, time.Second, 8, 8))
	for _, tc := range []struct {
		origin    string
		protocols []string
		status    int
	}{{"http://evil.example", []string{TerminalSubprotocol}, 403}, {testOrigin, nil, 400}} {
		_, response, err := websocket.Dial(context.Background(), sessionURL(server.URL, issued), dialOptions(tc.origin, tc.protocols))
		if err == nil || response == nil || response.StatusCode != tc.status {
			t.Fatalf("rejection response=%v err=%v", response, err)
		}
		body, _ := io.ReadAll(response.Body)
		_ = response.Body.Close()
		if strings.Contains(string(body), issued.Ticket) {
			t.Fatal("HTTP rejection echoed ticket")
		}
	}
	connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{TerminalSubprotocol})
	_ = connection.Close(websocket.StatusNormalClosure, "")
}

func TestInvalidBinaryFrameAndInboundBackpressureFailClosed(t *testing.T) {
	t.Run("binary", func(t *testing.T) {
		runtime := &fakeExecRuntime{}
		manager, issued := testManagerAndSession(t, 5*time.Second)
		server := testServer(t, testHandler(t, manager, runtime, 1024, time.Second, 8, 8))
		connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{TerminalSubprotocol})
		if err := connection.Write(context.Background(), websocket.MessageBinary, []byte("sensitive-payload")); err != nil {
			t.Fatal(err)
		}
		_, raw, err := connection.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if strings.Contains(string(raw), "sensitive-payload") || !strings.Contains(string(raw), "INVALID_TERMINAL_FRAME") {
			t.Fatalf("unsafe error frame: %s", raw)
		}
		_, _, err = connection.Read(context.Background())
		if websocket.CloseStatus(err) != websocket.StatusInternalError {
			t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
		}
	})
	t.Run("backpressure", func(t *testing.T) {
		runtime := &fakeExecRuntime{blockInput: true}
		manager, issued := testManagerAndSession(t, 5*time.Second)
		server := testServer(t, testHandler(t, manager, runtime, 1024, time.Second, 4, 1))
		connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{TerminalSubprotocol})
		for range 4 {
			_ = connection.Write(context.Background(), websocket.MessageText, []byte(`{"type":"stdin","data":"x"}`))
		}
		sawCode := false
		for {
			_, raw, err := connection.Read(context.Background())
			if err != nil {
				if websocket.CloseStatus(err) != websocket.StatusInternalError {
					t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
				}
				break
			}
			if strings.Contains(string(raw), "BACKPRESSURE_LIMIT") {
				sawCode = true
				continue
			}
		}
		if !sawCode {
			t.Fatal("backpressure error frame was not observed")
		}
	})
}

func TestIdleTimeoutAndMessageLimit(t *testing.T) {
	t.Run("idle", func(t *testing.T) {
		runtime := &fakeExecRuntime{}
		manager, issued := testManagerAndSession(t, time.Second)
		server := testServer(t, testHandler(t, manager, runtime, 1024, 60*time.Millisecond, 8, 8))
		connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{TerminalSubprotocol})
		_, _, err := connection.Read(context.Background())
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("idle close status=%v err=%v", websocket.CloseStatus(err), err)
		}
	})
	t.Run("message limit", func(t *testing.T) {
		runtime := &fakeExecRuntime{}
		manager, issued := testManagerAndSession(t, time.Second)
		server := testServer(t, testHandler(t, manager, runtime, 128, time.Second, 8, 8))
		connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{TerminalSubprotocol})
		payload := `{"type":"stdin","data":"` + strings.Repeat("x", 256) + `"}`
		_ = connection.Write(context.Background(), websocket.MessageText, []byte(payload))
		_, _, err := connection.Read(context.Background())
		if websocket.CloseStatus(err) != websocket.StatusMessageTooBig {
			t.Fatalf("limit close status=%v err=%v", websocket.CloseStatus(err), err)
		}
	})
	t.Run("max duration", func(t *testing.T) {
		runtime := &fakeExecRuntime{}
		manager, issued := testManagerAndSession(t, 80*time.Millisecond)
		server := testServer(t, testHandler(t, manager, runtime, 1024, 5*time.Second, 8, 8))
		connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{TerminalSubprotocol})
		_, _, err := connection.Read(context.Background())
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("max-duration close status=%v err=%v", websocket.CloseStatus(err), err)
		}
	})
}

func TestRuntimeOpenErrorIsSanitized(t *testing.T) {
	runtime := &fakeExecRuntime{openErr: errors.New("credential=super-secret")}
	manager, issued := testManagerAndSession(t, time.Second)
	server := testServer(t, testHandler(t, manager, runtime, 1024, time.Second, 8, 8))
	connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{TerminalSubprotocol})
	_, raw, err := connection.Read(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "super-secret") || !strings.Contains(string(raw), "RUNTIME_UNAVAILABLE") {
		t.Fatalf("unsafe runtime error frame: %s", raw)
	}
}

type fakeExecRuntime struct {
	mu                          sync.Mutex
	stream                      *fakeExecStream
	completeOnInput, blockInput bool
	openErr                     error
}

func (r *fakeExecRuntime) OpenExec(context.Context, runtimeport.ExecTarget, session.TerminalSize) (runtimeport.ExecStream, error) {
	if r.openErr != nil {
		return nil, r.openErr
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	r.stream = newFakeExecStream(r.completeOnInput, r.blockInput)
	return r.stream, nil
}
func (r *fakeExecRuntime) waitStream(t *testing.T) *fakeExecStream {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		stream := r.stream
		r.mu.Unlock()
		if stream != nil {
			return stream
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("exec runtime was not opened")
	return nil
}

type fakeExecStream struct {
	stdoutR                     *io.PipeReader
	stdoutW                     *io.PipeWriter
	done                        chan struct{}
	resizes                     chan session.TerminalSize
	once                        sync.Once
	completeOnInput, blockInput bool
	resultErr                   error
}

func newFakeExecStream(complete, block bool) *fakeExecStream {
	stdoutR, stdoutW := io.Pipe()
	return &fakeExecStream{stdoutR: stdoutR, stdoutW: stdoutW, done: make(chan struct{}), resizes: make(chan session.TerminalSize, 8), completeOnInput: complete, blockInput: block}
}
func (s *fakeExecStream) WriteStdin(data []byte) error {
	if s.blockInput {
		<-s.done
		return context.Canceled
	}
	if _, err := s.stdoutW.Write(data); err != nil {
		return err
	}
	if s.completeOnInput {
		s.finish(nil)
	}
	return nil
}
func (s *fakeExecStream) ReadStdout(data []byte) (int, error) { return s.stdoutR.Read(data) }
func (*fakeExecStream) ReadStderr([]byte) (int, error)        { return 0, io.EOF }
func (s *fakeExecStream) Resize(size session.TerminalSize) error {
	select {
	case s.resizes <- size:
		return nil
	default:
		return runtimeport.ErrBackpressure
	}
}
func (s *fakeExecStream) Wait() (int, error) { <-s.done; return 0, s.resultErr }
func (s *fakeExecStream) Close() error       { s.finish(context.Canceled); return nil }
func (s *fakeExecStream) finish(err error) {
	s.once.Do(func() { s.resultErr = err; _ = s.stdoutW.Close(); close(s.done) })
}

func testManagerAndSession(t *testing.T, maxDuration time.Duration) (*session.Manager, session.Issued) {
	t.Helper()
	base, _ := url.Parse("ws://unused.example/api/v1/realtime")
	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	manager, err := session.NewManager(memory.New(), session.ManagerConfig{PublicWSBaseURL: base, TicketKey: key, TicketTTL: time.Minute, SessionMaxDuration: maxDuration, IdempotencyTTL: 15 * time.Minute, MaxActive: 100, MaxActivePerSubject: 5})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := manager.Issue(context.Background(), session.Request{IdempotencyKey: t.Name(), TenantID: "tenant-a", SubjectID: "subject-a", InstanceID: "instance-a", WorkloadName: "workload-a", WorkloadKind: session.WorkloadContainer, Mode: session.ModeExec, Command: []string{"/bin/sh"}, TTY: true, Rows: 24, Cols: 80})
	if err != nil {
		t.Fatal(err)
	}
	return manager, issued
}
func testHandler(t *testing.T, manager *session.Manager, runtime runtimeport.ExecRuntime, maxBytes int64, idle time.Duration, outbound, inbound int) *Handler {
	t.Helper()
	handler, err := NewHandler(manager, runtime, nil, Config{AllowedOrigins: map[string]struct{}{testOrigin: {}}, MaxMessageBytes: maxBytes, IdleTimeout: idle, WriteTimeout: 200 * time.Millisecond, OutboundQueue: outbound, InboundQueue: inbound})
	if err != nil {
		t.Fatal(err)
	}
	return handler
}
func testServer(t *testing.T, handler *Handler) *httptest.Server {
	t.Helper()
	probes := observability.NewHandler()
	probes.RegisterStore("memory", true)
	probes.SetReady(true)
	router := httptransport.NewRouter(probes)
	RegisterRoutes(router, handler)
	server := httptest.NewServer(router)
	t.Cleanup(server.Close)
	return server
}
func sessionURL(serverURL string, issued session.Issued) string {
	return "ws" + strings.TrimPrefix(serverURL, "http") + "/api/v1/realtime/sessions/" + url.PathEscape(issued.Session.ID) + "?ticket=" + url.QueryEscape(issued.Ticket)
}
func dialOptions(origin string, protocols []string) *websocket.DialOptions {
	return &websocket.DialOptions{HTTPHeader: http.Header{"Origin": []string{origin}}, Subprotocols: protocols}
}
func dial(t *testing.T, rawURL, origin string, protocols []string) *websocket.Conn {
	t.Helper()
	connection, response, err := websocket.Dial(context.Background(), rawURL, dialOptions(origin, protocols))
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.CloseNow() })
	return connection
}
func writeJSON(t *testing.T, connection *websocket.Conn, frame clientFrame) {
	t.Helper()
	raw, _ := json.Marshal(frame)
	if err := connection.Write(context.Background(), websocket.MessageText, raw); err != nil {
		t.Fatal(err)
	}
}
func readServerFrames(t *testing.T, connection *websocket.Conn, count int) []serverFrame {
	t.Helper()
	frames := make([]serverFrame, 0, count)
	for len(frames) < count {
		messageType, raw, err := connection.Read(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		if messageType != websocket.MessageText {
			t.Fatalf("message type=%v", messageType)
		}
		var frame serverFrame
		if err := json.Unmarshal(raw, &frame); err != nil {
			t.Fatal(err)
		}
		frames = append(frames, frame)
	}
	return frames
}
