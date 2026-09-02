package kubevirtruntime

import (
	"bytes"
	"context"
	"io"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/observability"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	kubernetesruntime "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime/kubernetes"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	"k8s.io/client-go/rest"
	kubevirtv1 "kubevirt.io/api/core/v1"
	kvcorev1 "kubevirt.io/client-go/kubevirt/typed/core/v1"
)

const (
	tenantLabel   = "ani.kubercloud.io/tenant-id"
	instanceLabel = "ani.kubercloud.io/instance"
)

type vmiState struct {
	namespace, name, phase string
	labels                 map[string]string
	deleting               bool
}

type providerStream interface {
	Stream(kvcorev1.StreamOptions) error
	AsConn() net.Conn
}

type provider interface {
	Get(context.Context, string, string) (vmiState, error)
	OpenSerial(string, string) (providerStream, error)
	OpenVNC(string, string) (providerStream, error)
}

type kubeVirtProvider struct {
	config *rest.Config
	client *kvcorev1.KubevirtV1Client
}

func newKubeVirtProvider(config *rest.Config) (*kubeVirtProvider, error) {
	client, err := kvcorev1.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &kubeVirtProvider{config: rest.CopyConfig(config), client: client}, nil
}

func (p *kubeVirtProvider) Get(ctx context.Context, namespace, name string) (vmiState, error) {
	vmi, err := p.client.VirtualMachineInstances(namespace).Get(ctx, name, metav1.GetOptions{})
	if err != nil {
		return vmiState{}, err
	}
	return vmiState{namespace: vmi.Namespace, name: vmi.Name, phase: string(vmi.Status.Phase), labels: vmi.Labels, deleting: vmi.DeletionTimestamp != nil}, nil
}

func (p *kubeVirtProvider) OpenSerial(namespace, name string) (providerStream, error) {
	return kvcorev1.AsyncSubresourceHelper(p.config, "virtualmachineinstances", namespace, name, "console", url.Values{})
}

func (p *kubeVirtProvider) OpenVNC(namespace, name string) (providerStream, error) {
	return kvcorev1.AsyncSubresourceHelper(p.config, "virtualmachineinstances", namespace, name, "vnc", url.Values{"preserveSession": []string{"false"}})
}

type Adapter struct {
	provider          provider
	connectionTimeout time.Duration
}

func NewAdapter(config *rest.Config, connectionTimeout time.Duration) (*Adapter, error) {
	if config == nil || connectionTimeout <= 0 {
		return nil, runtimeport.ErrInvalidTarget
	}
	provider, err := newKubeVirtProvider(config)
	if err != nil {
		return nil, err
	}
	return &Adapter{provider: provider, connectionTimeout: connectionTimeout}, nil
}

func (a *Adapter) OpenSerial(ctx context.Context, target runtimeport.VMTarget) (runtimeport.ByteStream, error) {
	ctx, span := observability.StartSpan(ctx, "runtime.kubevirt.open_serial")
	defer span.End()
	return a.open(ctx, target, a.provider.OpenSerial)
}

func (a *Adapter) OpenVNC(ctx context.Context, target runtimeport.VMTarget) (runtimeport.ByteStream, error) {
	ctx, span := observability.StartSpan(ctx, "runtime.kubevirt.open_vnc")
	defer span.End()
	return a.open(ctx, target, a.provider.OpenVNC)
}

func (a *Adapter) open(ctx context.Context, target runtimeport.VMTarget, connect func(string, string) (providerStream, error)) (runtimeport.ByteStream, error) {
	namespace, err := kubernetesruntime.NamespaceForTenant(target.TenantID)
	if err != nil || target.WorkloadName == "" || len(validation.IsDNS1123Subdomain(target.WorkloadName)) != 0 {
		return nil, runtimeport.ErrInvalidTarget
	}
	state, err := a.provider.Get(ctx, namespace, target.WorkloadName)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, runtimeport.ErrTargetNotFound
		}
		return nil, runtimeport.ErrUnavailable
	}
	if state.namespace != namespace || state.name != target.WorkloadName || state.labels[tenantLabel] != target.TenantID || state.labels[instanceLabel] != target.WorkloadName {
		return nil, runtimeport.ErrTargetNotFound
	}
	if state.deleting || state.phase != string(kubevirtv1.Running) {
		return nil, runtimeport.ErrTargetNotReady
	}
	connectCtx, cancel := context.WithTimeout(ctx, a.connectionTimeout)
	defer cancel()
	type result struct {
		stream providerStream
		err    error
	}
	resultCh := make(chan result, 1)
	go func() {
		stream, err := connect(namespace, target.WorkloadName)
		if connectCtx.Err() != nil {
			if stream != nil {
				disposeProviderStream(stream)
			}
			return
		}
		select {
		case resultCh <- result{stream: stream, err: err}:
		case <-connectCtx.Done():
			if stream != nil {
				disposeProviderStream(stream)
			}
		}
	}()
	select {
	case <-connectCtx.Done():
		return nil, connectCtx.Err()
	case connected := <-resultCh:
		if connected.err != nil || connected.stream == nil {
			return nil, runtimeport.ErrUnavailable
		}
		if connected.stream.AsConn() == nil {
			return nil, runtimeport.ErrUnavailable
		}
		if connectCtx.Err() != nil {
			disposeProviderStream(connected.stream)
			return nil, connectCtx.Err()
		}
		return newManagedStream(ctx, connected.stream), nil
	}
}

type managedStream struct {
	provider providerStream
	stdinR   *io.PipeReader
	stdinW   *io.PipeWriter
	stdoutR  *io.PipeReader
	stdoutW  *io.PipeWriter
	done     chan struct{}
	once     sync.Once
}

func newManagedStream(ctx context.Context, provider providerStream) *managedStream {
	stdinR, stdinW := io.Pipe()
	stdoutR, stdoutW := io.Pipe()
	stream := &managedStream{provider: provider, stdinR: stdinR, stdinW: stdinW, stdoutR: stdoutR, stdoutW: stdoutW, done: make(chan struct{})}
	go func() {
		err := provider.Stream(kvcorev1.StreamOptions{In: stdinR, Out: stdoutW})
		if err != nil {
			_ = stdoutW.CloseWithError(err)
			_ = stdinR.CloseWithError(err)
		} else {
			_ = stdoutW.Close()
			_ = stdinR.Close()
		}
		close(stream.done)
	}()
	go func() {
		select {
		case <-ctx.Done():
			_ = stream.Close()
		case <-stream.done:
		}
	}()
	return stream
}

func (s *managedStream) Read(data []byte) (int, error)  { return s.stdoutR.Read(data) }
func (s *managedStream) Write(data []byte) (int, error) { return s.stdinW.Write(data) }

func (s *managedStream) Close() error {
	var err error
	s.once.Do(func() {
		_ = s.stdinW.Close()
		_ = s.stdoutR.Close()
		err = s.provider.AsConn().Close()
	})
	return err
}

func disposeProviderStream(stream providerStream) {
	_ = stream.AsConn().Close()
	_ = stream.Stream(kvcorev1.StreamOptions{In: bytes.NewReader(nil), Out: io.Discard})
}

var _ runtimeport.VMConsoleRuntime = (*Adapter)(nil)
var _ runtimeport.ByteStream = (*managedStream)(nil)
