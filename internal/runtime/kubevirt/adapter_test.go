package kubevirtruntime

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"
	kvcorev1 "kubevirt.io/client-go/kubevirt/typed/core/v1"
)

type fakeProviderStream struct{ connection net.Conn }

func (s fakeProviderStream) AsConn() net.Conn { return s.connection }
func (s fakeProviderStream) Stream(options kvcorev1.StreamOptions) error {
	result := make(chan error, 2)
	go func() { _, err := io.Copy(s.connection, options.In); result <- err }()
	go func() { _, err := io.Copy(options.Out, s.connection); result <- err }()
	return <-result
}

type fakeProvider struct {
	state      vmiState
	getErr     error
	openErr    error
	blockOpen  <-chan struct{}
	peer       net.Conn
	openedMode string
	openedNS   string
	openedName string
	latePeer   chan net.Conn
}

func (p *fakeProvider) Get(context.Context, string, string) (vmiState, error) {
	return p.state, p.getErr
}

func (p *fakeProvider) open(namespace, name, mode string) (providerStream, error) {
	p.openedNS, p.openedName, p.openedMode = namespace, name, mode
	if p.blockOpen != nil {
		<-p.blockOpen
	}
	if p.openErr != nil {
		return nil, p.openErr
	}
	connection, peer := net.Pipe()
	p.peer = peer
	if p.latePeer != nil {
		p.latePeer <- peer
	}
	return fakeProviderStream{connection: connection}, nil
}

func (p *fakeProvider) OpenSerial(namespace, name string) (providerStream, error) {
	return p.open(namespace, name, "serial")
}

func (p *fakeProvider) OpenVNC(namespace, name string) (providerStream, error) {
	return p.open(namespace, name, "vnc")
}

func runningProvider() *fakeProvider {
	return &fakeProvider{state: vmiState{namespace: "ani-tenant-tenant-a", name: "vm-a", phase: "Running", labels: map[string]string{tenantLabel: "tenant-a", instanceLabel: "vm-a"}}}
}

func TestAdapterSerialAndVNCPreserveBytes(t *testing.T) {
	for _, mode := range []string{"serial", "vnc"} {
		t.Run(mode, func(t *testing.T) {
			provider := runningProvider()
			adapter := &Adapter{provider: provider, connectionTimeout: time.Second}
			var stream runtimeport.ByteStream
			var err error
			if mode == "serial" {
				stream, err = adapter.OpenSerial(context.Background(), runtimeport.VMTarget{TenantID: "tenant-a", WorkloadName: "vm-a"})
			} else {
				stream, err = adapter.OpenVNC(context.Background(), runtimeport.VMTarget{TenantID: "tenant-a", WorkloadName: "vm-a"})
			}
			if err != nil {
				t.Fatal(err)
			}
			defer stream.Close()
			defer provider.peer.Close()
			input := []byte{0x00, 0x01, 0x7f, 0x80, 0xff}
			writeErr := make(chan error, 1)
			go func() { _, err := stream.Write(input); writeErr <- err }()
			got := make([]byte, len(input))
			if _, err := io.ReadFull(provider.peer, got); err != nil {
				t.Fatal(err)
			}
			if err := <-writeErr; err != nil || string(got) != string(input) {
				t.Fatalf("write err=%v bytes=%v", err, got)
			}
			go func() { _, _ = provider.peer.Write(input) }()
			if _, err := io.ReadFull(stream, got); err != nil || string(got) != string(input) {
				t.Fatalf("read err=%v bytes=%v", err, got)
			}
			if provider.openedMode != mode || provider.openedNS != "ani-tenant-tenant-a" || provider.openedName != "vm-a" {
				t.Fatalf("opened mode=%q namespace=%q name=%q", provider.openedMode, provider.openedNS, provider.openedName)
			}
		})
	}
}

func TestAdapterFailsClosedForVMIStateAndIdentity(t *testing.T) {
	target := runtimeport.VMTarget{TenantID: "tenant-a", WorkloadName: "vm-a"}
	tests := []struct {
		name     string
		provider *fakeProvider
		want     error
	}{
		{"not found", &fakeProvider{getErr: apierrors.NewNotFound(schema.GroupResource{Group: "kubevirt.io", Resource: "virtualmachineinstances"}, "vm-a")}, runtimeport.ErrTargetNotFound},
		{"not running", &fakeProvider{state: vmiState{namespace: "ani-tenant-tenant-a", name: "vm-a", phase: "Pending", labels: map[string]string{tenantLabel: "tenant-a", instanceLabel: "vm-a"}}}, runtimeport.ErrTargetNotReady},
		{"deleting", &fakeProvider{state: vmiState{namespace: "ani-tenant-tenant-a", name: "vm-a", phase: "Running", deleting: true, labels: map[string]string{tenantLabel: "tenant-a", instanceLabel: "vm-a"}}}, runtimeport.ErrTargetNotReady},
		{"cross tenant", &fakeProvider{state: vmiState{namespace: "ani-tenant-tenant-a", name: "vm-a", phase: "Running", labels: map[string]string{tenantLabel: "tenant-b", instanceLabel: "vm-a"}}}, runtimeport.ErrTargetNotFound},
		{"wrong identity", &fakeProvider{state: vmiState{namespace: "ani-tenant-tenant-a", name: "other", phase: "Running", labels: map[string]string{tenantLabel: "tenant-a", instanceLabel: "vm-a"}}}, runtimeport.ErrTargetNotFound},
		{"get unavailable", &fakeProvider{getErr: errors.New("api unavailable")}, runtimeport.ErrUnavailable},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			adapter := &Adapter{provider: test.provider, connectionTimeout: time.Second}
			if _, err := adapter.OpenSerial(context.Background(), target); !errors.Is(err, test.want) {
				t.Fatalf("error=%v want=%v", err, test.want)
			}
		})
	}
	adapter := &Adapter{provider: runningProvider(), connectionTimeout: time.Second}
	if _, err := adapter.OpenVNC(context.Background(), runtimeport.VMTarget{TenantID: "other", WorkloadName: "vm-a"}); !errors.Is(err, runtimeport.ErrTargetNotFound) {
		t.Fatalf("cross-namespace error=%v", err)
	}
}

func TestAdapterConnectionTimeoutAndCancellationCloseStream(t *testing.T) {
	block := make(chan struct{})
	latePeer := make(chan net.Conn, 1)
	provider := runningProvider()
	provider.blockOpen = block
	provider.latePeer = latePeer
	adapter := &Adapter{provider: provider, connectionTimeout: 20 * time.Millisecond}
	if _, err := adapter.OpenSerial(context.Background(), runtimeport.VMTarget{TenantID: "tenant-a", WorkloadName: "vm-a"}); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("timeout error=%v", err)
	}
	close(block)
	peer := <-latePeer
	defer peer.Close()
	_ = peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("late connection was not closed: %v", err)
	}

	provider = runningProvider()
	adapter = &Adapter{provider: provider, connectionTimeout: time.Second}
	ctx, cancel := context.WithCancel(context.Background())
	stream, err := adapter.OpenVNC(ctx, runtimeport.VMTarget{TenantID: "tenant-a", WorkloadName: "vm-a"})
	if err != nil {
		t.Fatal(err)
	}
	cancel()
	_ = provider.peer.SetReadDeadline(time.Now().Add(time.Second))
	if _, err := provider.peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("canceled stream was not closed: %v", err)
	}
	_ = stream.Close()
}

func TestAdapterMapsOpenFailureAndAbnormalClose(t *testing.T) {
	provider := runningProvider()
	provider.openErr = errors.New("credential=must-not-escape")
	adapter := &Adapter{provider: provider, connectionTimeout: time.Second}
	if _, err := adapter.OpenVNC(context.Background(), runtimeport.VMTarget{TenantID: "tenant-a", WorkloadName: "vm-a"}); !errors.Is(err, runtimeport.ErrUnavailable) {
		t.Fatalf("open error=%v", err)
	}
	provider = runningProvider()
	adapter = &Adapter{provider: provider, connectionTimeout: time.Second}
	stream, err := adapter.OpenSerial(context.Background(), runtimeport.VMTarget{TenantID: "tenant-a", WorkloadName: "vm-a"})
	if err != nil {
		t.Fatal(err)
	}
	_ = provider.peer.Close()
	if _, err := stream.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
		t.Fatalf("abnormal close error=%v", err)
	}
	_ = stream.Close()
}

func TestProductionProviderUsesPinnedKubeVirtSubresources(t *testing.T) {
	type requestEvidence struct{ path, protocol, query string }
	evidence := make(chan requestEvidence, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "/apis/kubevirt.io/v1/") {
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode(&kubevirtv1.VirtualMachineInstance{TypeMeta: metav1.TypeMeta{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachineInstance"}, ObjectMeta: metav1.ObjectMeta{Namespace: "ani-tenant-tenant-a", Name: "vm-a", Labels: map[string]string{tenantLabel: "tenant-a", instanceLabel: "vm-a"}}, Status: kubevirtv1.VirtualMachineInstanceStatus{Phase: kubevirtv1.Running}})
			return
		}
		connection, err := websocket.Accept(w, request, &websocket.AcceptOptions{Subprotocols: []string{"plain.kubevirt.io"}, CompressionMode: websocket.CompressionDisabled})
		if err != nil {
			t.Errorf("upgrade: %v", err)
			return
		}
		defer connection.CloseNow()
		evidence <- requestEvidence{path: request.URL.Path, protocol: connection.Subprotocol(), query: request.URL.RawQuery}
		messageType, payload, err := connection.Read(request.Context())
		if err == nil {
			_ = connection.Write(request.Context(), messageType, payload)
		}
	}))
	defer server.Close()
	adapter, err := NewAdapter(&rest.Config{Host: server.URL}, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, mode := range []string{"console", "vnc"} {
		var stream runtimeport.ByteStream
		if mode == "console" {
			stream, err = adapter.OpenSerial(context.Background(), runtimeport.VMTarget{TenantID: "tenant-a", WorkloadName: "vm-a"})
		} else {
			stream, err = adapter.OpenVNC(context.Background(), runtimeport.VMTarget{TenantID: "tenant-a", WorkloadName: "vm-a"})
		}
		if err != nil {
			t.Fatal(err)
		}
		payload := []byte{0x00, 0x52, 0x46, 0x42, 0xff}
		writeErr := make(chan error, 1)
		go func() { _, err := stream.Write(payload); writeErr <- err }()
		got := make([]byte, len(payload))
		if _, err := io.ReadFull(stream, got); err != nil || string(got) != string(payload) {
			t.Fatalf("mode=%s payload=%v err=%v", mode, got, err)
		}
		if err := <-writeErr; err != nil {
			t.Fatal(err)
		}
		_ = stream.Close()
		request := <-evidence
		wantSuffix := "/virtualmachineinstances/vm-a/" + mode
		if !strings.HasSuffix(request.path, wantSuffix) || request.protocol != "plain.kubevirt.io" {
			t.Fatalf("request=%#v", request)
		}
		if mode == "vnc" && request.query != "preserveSession=false" {
			t.Fatalf("VNC query=%q", request.query)
		}
	}
}
