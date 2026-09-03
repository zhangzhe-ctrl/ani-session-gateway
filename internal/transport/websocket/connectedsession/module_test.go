package connectedsession_test

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/memory"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/websocket/connectedsession"
)

func TestSerialInvalidFrameEndsOnceAsProtocolFailure(t *testing.T) {
	manager, access := claimedAccess(t, session.ModeSerial)
	vm := newVMRuntime()
	observer := newRecordingObserver()
	module, err := connectedsession.New(connectedsession.Dependencies{
		Manager:  manager,
		Exec:     unusedExecRuntime{},
		VM:       vm,
		Clock:    connectedsession.SystemClock{},
		Observer: observer,
	}, connectedsession.Policy{
		MaxMessageBytes: 1024,
		IdleTimeout:     time.Minute,
		WriteTimeout:    time.Second,
		InboundQueue:    8,
		OutboundQueue:   8,
	})
	if err != nil {
		t.Fatal(err)
	}

	resultCh := make(chan connectedsession.Outcome, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(w, request, &websocket.AcceptOptions{Subprotocols: []string{"ani.terminal.v1"}, InsecureSkipVerify: true})
		if err != nil {
			return
		}
		resultCh <- module.Run(request.Context(), connectedsession.Accepted{Access: access, Socket: conn})
	}))
	t.Cleanup(server.Close)

	wsURL := "ws" + server.URL[len("http"):]
	conn, response, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{Subprotocols: []string{"ani.terminal.v1"}})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	observer.waitConnected(t)
	if err := conn.Write(context.Background(), websocket.MessageText, []byte("ls")); err != nil {
		t.Fatal(err)
	}
	messageType, raw, err := conn.Read(context.Background())
	if err != nil || messageType != websocket.MessageText {
		t.Fatalf("error frame type=%v err=%v", messageType, err)
	}
	var frame struct {
		Type    string `json:"type"`
		Code    string `json:"code"`
		Message string `json:"message"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil {
		t.Fatal(err)
	}
	if frame.Type != "error" || frame.Code != "INVALID_TERMINAL_FRAME" || frame.Message != "invalid terminal frame" {
		t.Fatalf("error frame=%s", raw)
	}
	if _, _, err := conn.Read(context.Background()); websocket.CloseStatus(err) != websocket.StatusInternalError {
		t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
	}
	select {
	case outcome := <-resultCh:
		if outcome != connectedsession.OutcomeInvalidTerminalFrame {
			t.Fatalf("outcome=%q", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("Connected Session did not finish")
	}
	finish := observer.finish(t)
	if observer.connectedCount() != 1 || observer.finishCount() != 1 || finish.Outcome != connectedsession.OutcomeInvalidTerminalFrame {
		t.Fatalf("observations connected=%d finished=%d finish=%#v", observer.connectedCount(), observer.finishCount(), finish)
	}
}

func TestSerialCarriesPayloadAndCountsRuntimeBytes(t *testing.T) {
	manager, access := claimedAccess(t, session.ModeSerial)
	vm := newVMRuntime()
	observer := newRecordingObserver()
	module := newTestModule(t, manager, vm, observer)
	resultCh, conn := serveConnectedSession(t, module, access, "ani.terminal.v1")

	observer.waitConnected(t)
	peer := vm.waitPeer(t)
	defer peer.Close()
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"stdin","data":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	input := make([]byte, len("ping"))
	if _, err := io.ReadFull(peer, input); err != nil || string(input) != "ping" {
		t.Fatalf("runtime input=%q err=%v", input, err)
	}
	go func() { _, _ = peer.Write([]byte("pong")) }()
	messageType, raw, err := conn.Read(context.Background())
	if err != nil || messageType != websocket.MessageText {
		t.Fatalf("browser output type=%v err=%v", messageType, err)
	}
	var frame struct {
		Type string `json:"type"`
		Data string `json:"data"`
	}
	if err := json.Unmarshal(raw, &frame); err != nil || frame.Type != "stdout" || frame.Data != "pong" {
		t.Fatalf("browser output=%s err=%v", raw, err)
	}
	if err := conn.Close(websocket.StatusNormalClosure, ""); err != nil {
		t.Fatal(err)
	}
	select {
	case outcome := <-resultCh:
		if outcome != connectedsession.OutcomeClientClosed {
			t.Fatalf("outcome=%q", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("Connected Session did not finish")
	}
	finish := observer.finish(t)
	if finish.BytesIn != 4 || finish.BytesOut != 4 {
		t.Fatalf("payload bytes in=%d out=%d", finish.BytesIn, finish.BytesOut)
	}
}

func TestExecCarriesTerminalFramesAndEndsNormally(t *testing.T) {
	manager, access := claimedAccess(t, session.ModeExec)
	exec := &execRuntime{}
	observer := newRecordingObserver()
	module := newTestModuleWithRuntimes(t, manager, exec, newVMRuntime(), observer)
	resultCh, conn := serveConnectedSession(t, module, access, "ani.terminal.v1")

	observer.waitConnected(t)
	stream := exec.waitStream(t)
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"resize","rows":30,"cols":120}`)); err != nil {
		t.Fatal(err)
	}
	select {
	case size := <-stream.resizes:
		if size != (session.TerminalSize{Rows: 30, Cols: 120}) {
			t.Fatalf("resize=%#v", size)
		}
	case <-time.After(time.Second):
		t.Fatal("resize was not forwarded")
	}
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(`{"type":"stdin","data":"ping"}`)); err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"stdout", "exit"} {
		messageType, raw, err := conn.Read(context.Background())
		if err != nil || messageType != websocket.MessageText {
			t.Fatalf("%s frame type=%v err=%v", want, messageType, err)
		}
		var frame struct {
			Type string `json:"type"`
			Data string `json:"data"`
			Code int    `json:"code"`
		}
		if err := json.Unmarshal(raw, &frame); err != nil || frame.Type != want {
			t.Fatalf("%s frame=%s err=%v", want, raw, err)
		}
		if want == "stdout" && frame.Data != "ping" {
			t.Fatalf("stdout=%q", frame.Data)
		}
	}
	if _, _, err := conn.Read(context.Background()); websocket.CloseStatus(err) != websocket.StatusNormalClosure {
		t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
	}
	select {
	case outcome := <-resultCh:
		if outcome != connectedsession.OutcomeNormal {
			t.Fatalf("outcome=%q", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("Connected Session did not finish")
	}
	finish := observer.finish(t)
	if finish.BytesIn != 4 || finish.BytesOut != 4 {
		t.Fatalf("payload bytes in=%d out=%d", finish.BytesIn, finish.BytesOut)
	}
}

func TestVNCPreservesBinaryPayloadAndRejectsText(t *testing.T) {
	t.Run("binary payload", func(t *testing.T) {
		manager, access := claimedAccess(t, session.ModeVNC)
		vm := newVMRuntime()
		observer := newRecordingObserver()
		module := newTestModule(t, manager, vm, observer)
		_, conn := serveConnectedSession(t, module, access, "ani.vnc.v1")
		observer.waitConnected(t)
		peer := vm.waitPeer(t)
		defer peer.Close()
		rfb := []byte{0x52, 0x46, 0x42, 0x20, 0x00, 0x80, 0xff}
		if err := conn.Write(context.Background(), websocket.MessageBinary, rfb); err != nil {
			t.Fatal(err)
		}
		got := make([]byte, len(rfb))
		if _, err := io.ReadFull(peer, got); err != nil || string(got) != string(rfb) {
			t.Fatalf("runtime payload=%v err=%v", got, err)
		}
		go func() { _, _ = peer.Write(rfb) }()
		messageType, raw, err := conn.Read(context.Background())
		if err != nil || messageType != websocket.MessageBinary || string(raw) != string(rfb) {
			t.Fatalf("browser payload type=%v bytes=%v err=%v", messageType, raw, err)
		}
	})
	t.Run("text frame", func(t *testing.T) {
		manager, access := claimedAccess(t, session.ModeVNC)
		observer := newRecordingObserver()
		module := newTestModule(t, manager, newVMRuntime(), observer)
		resultCh, conn := serveConnectedSession(t, module, access, "ani.vnc.v1")
		observer.waitConnected(t)
		if err := conn.Write(context.Background(), websocket.MessageText, []byte("not-rfb")); err != nil {
			t.Fatal(err)
		}
		if _, _, err := conn.Read(context.Background()); websocket.CloseStatus(err) != websocket.StatusInternalError {
			t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
		}
		select {
		case outcome := <-resultCh:
			if outcome != connectedsession.OutcomeInvalidVNCFrame {
				t.Fatalf("outcome=%q", outcome)
			}
		case <-time.After(time.Second):
			t.Fatal("Connected Session did not finish")
		}
	})
}

func TestRuntimeOpenFailureNeverBecomesConnected(t *testing.T) {
	manager, access := claimedAccess(t, session.ModeSerial)
	observer := newRecordingObserver()
	module := newTestModule(t, manager, failingVMRuntime{err: errors.New("credential=super-secret")}, observer)
	resultCh, conn := serveConnectedSession(t, module, access, "ani.terminal.v1")
	messageType, raw, err := conn.Read(context.Background())
	if err != nil || messageType != websocket.MessageText {
		t.Fatalf("error frame type=%v err=%v", messageType, err)
	}
	if strings.Contains(string(raw), "super-secret") || !strings.Contains(string(raw), "RUNTIME_UNAVAILABLE") {
		t.Fatalf("unsafe error frame=%s", raw)
	}
	if _, _, err := conn.Read(context.Background()); websocket.CloseStatus(err) != websocket.StatusInternalError {
		t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
	}
	select {
	case outcome := <-resultCh:
		if outcome != connectedsession.OutcomeRuntimeUnavailable {
			t.Fatalf("outcome=%q", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("Connected Session did not finish")
	}
	finish := observer.finish(t)
	if observer.connectedCount() != 0 || finish.Connected {
		t.Fatalf("failed start was marked connected: count=%d finish=%#v", observer.connectedCount(), finish)
	}
}

func TestConcurrentSessionLimitsChooseOneOutcomeAndFinishOnce(t *testing.T) {
	manager, access := claimedAccess(t, session.ModeSerial)
	clock := newManualClock(time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC))
	access.ExpiresAt = clock.Now().Add(time.Minute)
	observer := newRecordingObserver()
	module := newTestModuleWithClock(t, manager, unusedExecRuntime{}, newVMRuntime(), clock, observer, time.Minute)
	resultCh, conn := serveConnectedSession(t, module, access, "ani.terminal.v1")
	observer.waitConnected(t)
	clock.waitActiveTimers(t, 2)
	clock.Advance(time.Minute)
	if _, _, err := conn.Read(context.Background()); websocket.CloseStatus(err) != websocket.StatusPolicyViolation {
		t.Fatalf("close status=%v err=%v", websocket.CloseStatus(err), err)
	}
	select {
	case outcome := <-resultCh:
		if outcome != connectedsession.OutcomeIdleTimeout && outcome != connectedsession.OutcomeMaxDuration {
			t.Fatalf("outcome=%q", outcome)
		}
	case <-time.After(time.Second):
		t.Fatal("Connected Session did not finish")
	}
	if observer.finishCount() != 1 || observer.connectedCount() != 1 {
		t.Fatalf("observations connected=%d finished=%d", observer.connectedCount(), observer.finishCount())
	}
}

func newTestModule(t *testing.T, manager *session.Manager, vm runtimeport.VMConsoleRuntime, observer connectedsession.Observer) *connectedsession.Module {
	t.Helper()
	return newTestModuleWithRuntimes(t, manager, unusedExecRuntime{}, vm, observer)
}

func newTestModuleWithRuntimes(t *testing.T, manager *session.Manager, exec runtimeport.ExecRuntime, vm runtimeport.VMConsoleRuntime, observer connectedsession.Observer) *connectedsession.Module {
	t.Helper()
	return newTestModuleWithClock(t, manager, exec, vm, connectedsession.SystemClock{}, observer, time.Minute)
}

func newTestModuleWithClock(t *testing.T, manager *session.Manager, exec runtimeport.ExecRuntime, vm runtimeport.VMConsoleRuntime, clock connectedsession.Clock, observer connectedsession.Observer, idle time.Duration) *connectedsession.Module {
	t.Helper()
	module, err := connectedsession.New(connectedsession.Dependencies{
		Manager: manager, Exec: exec, VM: vm, Clock: clock, Observer: observer,
	}, connectedsession.Policy{MaxMessageBytes: 1024, IdleTimeout: idle, WriteTimeout: time.Second, InboundQueue: 8, OutboundQueue: 8})
	if err != nil {
		t.Fatal(err)
	}
	return module
}

func serveConnectedSession(t *testing.T, module *connectedsession.Module, access session.ClaimedAccess, subprotocol string) (<-chan connectedsession.Outcome, *websocket.Conn) {
	t.Helper()
	resultCh := make(chan connectedsession.Outcome, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		conn, err := websocket.Accept(w, request, &websocket.AcceptOptions{Subprotocols: []string{subprotocol}, InsecureSkipVerify: true})
		if err != nil {
			return
		}
		resultCh <- module.Run(request.Context(), connectedsession.Accepted{Access: access, Socket: conn})
	}))
	t.Cleanup(server.Close)
	wsURL := "ws" + server.URL[len("http"):]
	conn, response, err := websocket.Dial(context.Background(), wsURL, &websocket.DialOptions{Subprotocols: []string{subprotocol}})
	if err != nil {
		if response != nil {
			_ = response.Body.Close()
		}
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })
	return resultCh, conn
}

func claimedAccess(t *testing.T, mode session.Mode) (*session.Manager, session.ClaimedAccess) {
	t.Helper()
	base, _ := url.Parse("ws://unused.example/api/v1/realtime")
	var key [32]byte
	copy(key[:], []byte("0123456789abcdef0123456789abcdef"))
	manager, err := session.NewManager(memory.New(), session.ManagerConfig{
		PublicWSBaseURL:     base,
		TicketKey:           key,
		TicketTTL:           time.Minute,
		SessionMaxDuration:  15 * time.Minute,
		IdempotencyTTL:      15 * time.Minute,
		MaxActive:           100,
		MaxActivePerSubject: 5,
	})
	if err != nil {
		t.Fatal(err)
	}
	request := session.Request{IdempotencyKey: t.Name(), TenantID: "tenant-a", SubjectID: "subject-a", InstanceID: "instance-a", WorkloadName: "vm-a", WorkloadKind: session.WorkloadVM, Mode: mode, RequestedProtocol: string(mode)}
	if mode == session.ModeExec {
		request.WorkloadKind = session.WorkloadContainer
		request.Command = []string{"/bin/sh"}
		request.TTY = true
		request.Rows, request.Cols = 24, 80
	}
	issued, err := manager.Issue(context.Background(), request)
	if err != nil {
		t.Fatal(err)
	}
	access, err := manager.Claim(context.Background(), issued.Session.ID, issued.Ticket)
	if err != nil {
		t.Fatal(err)
	}
	return manager, access
}

type unusedExecRuntime struct{}

func (unusedExecRuntime) OpenExec(context.Context, runtimeport.ExecTarget, session.TerminalSize) (runtimeport.ExecStream, error) {
	return nil, runtimeport.ErrUnavailable
}

type execRuntime struct {
	mu     sync.Mutex
	stream *execStream
}

func (r *execRuntime) OpenExec(context.Context, runtimeport.ExecTarget, session.TerminalSize) (runtimeport.ExecStream, error) {
	stream := newExecStream()
	r.mu.Lock()
	r.stream = stream
	r.mu.Unlock()
	return stream, nil
}

func (r *execRuntime) waitStream(t *testing.T) *execStream {
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

type execStream struct {
	stdoutR *io.PipeReader
	stdoutW *io.PipeWriter
	done    chan struct{}
	resizes chan session.TerminalSize
	once    sync.Once
}

func newExecStream() *execStream {
	stdoutR, stdoutW := io.Pipe()
	return &execStream{stdoutR: stdoutR, stdoutW: stdoutW, done: make(chan struct{}), resizes: make(chan session.TerminalSize, 1)}
}

func (s *execStream) WriteStdin(data []byte) error {
	if _, err := s.stdoutW.Write(data); err != nil {
		return err
	}
	s.once.Do(func() {
		_ = s.stdoutW.Close()
		close(s.done)
	})
	return nil
}
func (s *execStream) ReadStdout(data []byte) (int, error) { return s.stdoutR.Read(data) }
func (*execStream) ReadStderr([]byte) (int, error)        { return 0, io.EOF }
func (s *execStream) Resize(size session.TerminalSize) error {
	s.resizes <- size
	return nil
}
func (s *execStream) Wait() (int, error) { <-s.done; return 0, nil }
func (s *execStream) Close() error {
	s.once.Do(func() {
		_ = s.stdoutW.Close()
		close(s.done)
	})
	return s.stdoutR.Close()
}

type vmRuntime struct {
	mu   sync.Mutex
	peer net.Conn
}

type failingVMRuntime struct{ err error }

func (r failingVMRuntime) OpenSerial(context.Context, runtimeport.VMTarget) (runtimeport.ByteStream, error) {
	return nil, r.err
}
func (r failingVMRuntime) OpenVNC(context.Context, runtimeport.VMTarget) (runtimeport.ByteStream, error) {
	return nil, r.err
}

func newVMRuntime() *vmRuntime { return &vmRuntime{} }

func (r *vmRuntime) open() runtimeport.ByteStream {
	stream, peer := net.Pipe()
	r.mu.Lock()
	r.peer = peer
	r.mu.Unlock()
	return stream
}

func (r *vmRuntime) OpenSerial(context.Context, runtimeport.VMTarget) (runtimeport.ByteStream, error) {
	return r.open(), nil
}

func (r *vmRuntime) OpenVNC(context.Context, runtimeport.VMTarget) (runtimeport.ByteStream, error) {
	return r.open(), nil
}

func (r *vmRuntime) waitPeer(t *testing.T) net.Conn {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		r.mu.Lock()
		peer := r.peer
		r.mu.Unlock()
		if peer != nil {
			return peer
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("VM runtime was not opened")
	return nil
}

type recordingObserver struct {
	mu          sync.Mutex
	connected   int
	finishes    []connectedsession.Finish
	connectedCh chan struct{}
}

type manualClock struct {
	mu     sync.Mutex
	now    time.Time
	timers []*manualTimer
}

func newManualClock(now time.Time) *manualClock { return &manualClock{now: now} }

func (c *manualClock) Now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.now
}

func (c *manualClock) NewTimer(after time.Duration) connectedsession.Timer {
	c.mu.Lock()
	defer c.mu.Unlock()
	timer := &manualTimer{clock: c, ch: make(chan time.Time, 1), due: c.now.Add(after), active: true}
	c.timers = append(c.timers, timer)
	return timer
}

func (c *manualClock) Advance(delta time.Duration) {
	c.mu.Lock()
	c.now = c.now.Add(delta)
	for _, timer := range c.timers {
		if timer.active && !timer.due.After(c.now) {
			timer.active = false
			timer.ch <- c.now
		}
	}
	c.mu.Unlock()
}

func (c *manualClock) waitActiveTimers(t *testing.T, count int) {
	t.Helper()
	deadline := time.Now().Add(time.Second)
	for time.Now().Before(deadline) {
		c.mu.Lock()
		active := 0
		for _, timer := range c.timers {
			if timer.active {
				active++
			}
		}
		c.mu.Unlock()
		if active >= count {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("Connected Session timers were not armed")
}

type manualTimer struct {
	clock  *manualClock
	ch     chan time.Time
	due    time.Time
	active bool
}

func (t *manualTimer) C() <-chan time.Time { return t.ch }
func (t *manualTimer) Reset(after time.Duration) bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = true
	t.due = t.clock.now.Add(after)
	return wasActive
}
func (t *manualTimer) Stop() bool {
	t.clock.mu.Lock()
	defer t.clock.mu.Unlock()
	wasActive := t.active
	t.active = false
	return wasActive
}

func newRecordingObserver() *recordingObserver {
	return &recordingObserver{connectedCh: make(chan struct{}, 1)}
}

func (o *recordingObserver) Open(context.Context, connectedsession.SafeFacts) connectedsession.Observation {
	return o
}

func (o *recordingObserver) Connected(time.Time) {
	o.mu.Lock()
	o.connected++
	o.mu.Unlock()
	select {
	case o.connectedCh <- struct{}{}:
	default:
	}
}

func (o *recordingObserver) Finish(finish connectedsession.Finish) {
	o.mu.Lock()
	defer o.mu.Unlock()
	o.finishes = append(o.finishes, finish)
}

func (o *recordingObserver) waitConnected(t *testing.T) {
	t.Helper()
	select {
	case <-o.connectedCh:
	case <-time.After(time.Second):
		t.Fatal("Connected Session did not become active")
	}
}

func (o *recordingObserver) connectedCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return o.connected
}

func (o *recordingObserver) finishCount() int {
	o.mu.Lock()
	defer o.mu.Unlock()
	return len(o.finishes)
}

func (o *recordingObserver) finish(t *testing.T) connectedsession.Finish {
	t.Helper()
	o.mu.Lock()
	defer o.mu.Unlock()
	if len(o.finishes) != 1 {
		t.Fatalf("finish count=%d", len(o.finishes))
	}
	return o.finishes[0]
}

var _ io.ReadWriteCloser = (net.Conn)(nil)
