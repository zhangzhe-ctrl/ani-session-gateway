package websockettransport

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/memory"
)

type trackingStream struct {
	net.Conn
	closed chan struct{}
	once   sync.Once
}

func (s *trackingStream) Close() error {
	s.once.Do(func() { close(s.closed) })
	return s.Conn.Close()
}

type fakeVMRuntime struct {
	mu       sync.Mutex
	peer     net.Conn
	stream   *trackingStream
	mode     string
	openErr  error
	openedCh chan struct{}
}

func newFakeVMRuntime() *fakeVMRuntime { return &fakeVMRuntime{openedCh: make(chan struct{}, 1)} }

func (r *fakeVMRuntime) open(mode string) (runtimeport.ByteStream, error) {
	if r.openErr != nil {
		return nil, r.openErr
	}
	connection, peer := net.Pipe()
	stream := &trackingStream{Conn: connection, closed: make(chan struct{})}
	r.mu.Lock()
	r.peer, r.stream, r.mode = peer, stream, mode
	r.mu.Unlock()
	r.openedCh <- struct{}{}
	return stream, nil
}

func (r *fakeVMRuntime) OpenSerial(context.Context, runtimeport.VMTarget) (runtimeport.ByteStream, error) {
	return r.open("serial")
}

func (r *fakeVMRuntime) OpenVNC(context.Context, runtimeport.VMTarget) (runtimeport.ByteStream, error) {
	return r.open("vnc")
}

func (r *fakeVMRuntime) wait(t *testing.T) (net.Conn, *trackingStream, string) {
	t.Helper()
	select {
	case <-r.openedCh:
	case <-time.After(time.Second):
		t.Fatal("VM runtime was not opened")
	}
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.peer, r.stream, r.mode
}

func TestSerialJSONBridgeAndTicketReplay(t *testing.T) {
	vmRuntime := newFakeVMRuntime()
	manager, issued := testVMManagerAndSession(t, session.ModeSerial, 5*time.Second)
	handler := testHandler(t, manager, &fakeExecRuntime{}, 1024, time.Second, 8, 8)
	handler.vm = vmRuntime
	server := testServer(t, handler)
	connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{TerminalSubprotocol})
	peer, stream, mode := vmRuntime.wait(t)
	defer peer.Close()
	if mode != "serial" {
		t.Fatalf("mode=%q", mode)
	}
	writeJSON(t, connection, clientFrame{Type: "stdin", Data: "\n"})
	input := make([]byte, 1)
	if _, err := io.ReadFull(peer, input); err != nil || string(input) != "\n" {
		t.Fatalf("serial input=%q err=%v", input, err)
	}
	go func() { _, _ = peer.Write([]byte("login:")) }()
	messageType, raw, err := connection.Read(context.Background())
	if err != nil || messageType != websocket.MessageText {
		t.Fatalf("serial read type=%v err=%v", messageType, err)
	}
	var frame serverFrame
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Type != "stdout" || frame.Data != "login:" {
		t.Fatalf("serial frame=%s err=%v", raw, err)
	}
	_ = connection.Close(websocket.StatusNormalClosure, "")
	select {
	case <-stream.closed:
	case <-time.After(time.Second):
		t.Fatal("serial stream was not closed after client disconnect")
	}
	_, response, err := websocket.Dial(context.Background(), sessionURL(server.URL, issued), dialOptions(testOrigin, []string{TerminalSubprotocol}))
	if err == nil || response == nil || response.StatusCode != 422 {
		t.Fatalf("ticket replay response=%v err=%v", response, err)
	}
}

func TestVNCBridgePreservesBinaryRFB(t *testing.T) {
	vmRuntime := newFakeVMRuntime()
	manager, issued := testVMManagerAndSession(t, session.ModeVNC, 5*time.Second)
	handler := testHandler(t, manager, &fakeExecRuntime{}, 1024, time.Second, 8, 8)
	handler.vm = vmRuntime
	server := testServer(t, handler)
	connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{VNCSubprotocol})
	peer, _, mode := vmRuntime.wait(t)
	defer peer.Close()
	if mode != "vnc" {
		t.Fatalf("mode=%q", mode)
	}
	rfb := []byte{0x52, 0x46, 0x42, 0x20, 0x00, 0x80, 0xff}
	if err := connection.Write(context.Background(), websocket.MessageBinary, rfb); err != nil {
		t.Fatal(err)
	}
	got := make([]byte, len(rfb))
	if _, err := io.ReadFull(peer, got); err != nil || string(got) != string(rfb) {
		t.Fatalf("provider bytes=%v err=%v", got, err)
	}
	go func() { _, _ = peer.Write(rfb) }()
	messageType, raw, err := connection.Read(context.Background())
	if err != nil || messageType != websocket.MessageBinary || string(raw) != string(rfb) {
		t.Fatalf("browser type=%v bytes=%v err=%v", messageType, raw, err)
	}
	if strings.Contains(string(raw), "base64") {
		t.Fatal("VNC payload was wrapped instead of bridged transparently")
	}
}

func TestConsoleFailureTimeoutAndInvalidVNCFrame(t *testing.T) {
	t.Run("runtime failure sanitized", func(t *testing.T) {
		vmRuntime := newFakeVMRuntime()
		vmRuntime.openErr = errors.New("credential=super-secret")
		manager, issued := testVMManagerAndSession(t, session.ModeVNC, time.Second)
		handler := testHandler(t, manager, &fakeExecRuntime{}, 1024, time.Second, 8, 8)
		handler.vm = vmRuntime
		server := testServer(t, handler)
		connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{VNCSubprotocol})
		_, _, err := connection.Read(context.Background())
		if websocket.CloseStatus(err) != websocket.StatusInternalError || strings.Contains(err.Error(), "super-secret") {
			t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
		}
	})
	t.Run("text rejected for VNC", func(t *testing.T) {
		vmRuntime := newFakeVMRuntime()
		manager, issued := testVMManagerAndSession(t, session.ModeVNC, time.Second)
		handler := testHandler(t, manager, &fakeExecRuntime{}, 1024, time.Second, 8, 8)
		handler.vm = vmRuntime
		server := testServer(t, handler)
		connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{VNCSubprotocol})
		peer, _, _ := vmRuntime.wait(t)
		defer peer.Close()
		_ = connection.Write(context.Background(), websocket.MessageText, []byte("not-rfb"))
		_, _, err := connection.Read(context.Background())
		if websocket.CloseStatus(err) != websocket.StatusInternalError {
			t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
		}
	})
	t.Run("maximum duration", func(t *testing.T) {
		vmRuntime := newFakeVMRuntime()
		manager, issued := testVMManagerAndSession(t, session.ModeSerial, 80*time.Millisecond)
		handler := testHandler(t, manager, &fakeExecRuntime{}, 1024, time.Second, 8, 8)
		handler.vm = vmRuntime
		server := testServer(t, handler)
		connection := dial(t, sessionURL(server.URL, issued), testOrigin, []string{TerminalSubprotocol})
		peer, _, _ := vmRuntime.wait(t)
		defer peer.Close()
		_, _, err := connection.Read(context.Background())
		if websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
			t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
		}
	})
}

func testVMManagerAndSession(t *testing.T, mode session.Mode, maxDuration time.Duration) (*session.Manager, session.Issued) {
	t.Helper()
	base, _ := url.Parse("ws://unused.example/api/v1/realtime")
	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	manager, err := session.NewManager(memory.New(), session.ManagerConfig{PublicWSBaseURL: base, TicketKey: key, TicketTTL: time.Minute, SessionMaxDuration: maxDuration, IdempotencyTTL: 15 * time.Minute, MaxActive: 100, MaxActivePerSubject: 5})
	if err != nil {
		t.Fatal(err)
	}
	issued, err := manager.Issue(context.Background(), session.Request{IdempotencyKey: t.Name(), TenantID: "tenant-a", SubjectID: "subject-a", InstanceID: "vm-a", WorkloadName: "vm-a", WorkloadKind: session.WorkloadVM, Mode: mode, RequestedProtocol: string(mode)})
	if err != nil {
		t.Fatal(err)
	}
	return manager, issued
}
