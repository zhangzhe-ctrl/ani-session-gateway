package kubevirtruntime

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/url"
	"sync"
	"time"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/observability"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	kubernetesruntime "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime/kubernetes"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	apierrors "k8s.io/apimachinery/pkg/api/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/util/validation"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
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
	apiVersion, kind       string
	uid                    string
	owners                 []metav1.OwnerReference
	labels                 map[string]string
	deleting               bool
}

type vmIdentityProvider interface {
	ValidateVMIdentity(context.Context, session.VMBinding) error
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
	core   typedcorev1.CoreV1Interface
}

func newKubeVirtProvider(config *rest.Config) (*kubeVirtProvider, error) {
	client, err := kvcorev1.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	core, err := typedcorev1.NewForConfig(config)
	if err != nil {
		return nil, err
	}
	return &kubeVirtProvider{config: rest.CopyConfig(config), client: client, core: core}, nil
}

func (p *kubeVirtProvider) Get(ctx context.Context, namespace, name string) (vmiState, error) {
	// The generated typed client's decoder clears TypeMeta. Keep the GVK from
	// the native response so managed identity checks verify the wire identity.
	body, err := p.client.RESTClient().Get().Namespace(namespace).Resource("virtualmachineinstances").Name(name).Do(ctx).Raw()
	if err != nil {
		return vmiState{}, err
	}
	var vmi kubevirtv1.VirtualMachineInstance
	if err := json.Unmarshal(body, &vmi); err != nil {
		return vmiState{}, err
	}
	return vmiState{namespace: vmi.Namespace, name: vmi.Name, apiVersion: vmi.APIVersion, kind: vmi.Kind, uid: string(vmi.UID), owners: vmi.OwnerReferences, phase: string(vmi.Status.Phase), labels: vmi.Labels, deleting: vmi.DeletionTimestamp != nil}, nil
}

func (p *kubeVirtProvider) ValidateVMIdentity(ctx context.Context, b session.VMBinding) error {
	ns, err := p.core.Namespaces().Get(ctx, b.Namespace, metav1.GetOptions{})
	if err != nil {
		return identityReadError(err)
	}
	if string(ns.UID) != b.NamespaceUID || ns.DeletionTimestamp != nil {
		return runtimeport.ErrTargetChanged
	}
	body, err := p.client.RESTClient().Get().Namespace(b.Namespace).Resource("virtualmachines").Name(b.VMName).Do(ctx).Raw()
	if err != nil {
		return identityReadError(err)
	}
	var vm kubevirtv1.VirtualMachine
	if err := json.Unmarshal(body, &vm); err != nil {
		return runtimeport.ErrUnavailable
	}
	if vm.APIVersion != "kubevirt.io/v1" || vm.Kind != "VirtualMachine" || vm.Namespace != b.Namespace || vm.Name != b.VMName || string(vm.UID) != b.VMUID || vm.DeletionTimestamp != nil {
		return runtimeport.ErrTargetChanged
	}
	return nil
}

func identityReadError(err error) error {
	if apierrors.IsNotFound(err) {
		return runtimeport.ErrTargetChanged
	}
	return runtimeport.ErrUnavailable
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
	validator         session.VMValidator
	clusterID         string
}

func (a *Adapter) ConfigureVMValidator(validator session.VMValidator, clusterID string) {
	a.validator, a.clusterID = validator, clusterID
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
	if target.Binding.Enabled {
		return nil, runtimeport.ErrInvalidTarget
	}
	return a.open(ctx, target, a.provider.OpenSerial)
}

func (a *Adapter) OpenVNC(ctx context.Context, target runtimeport.VMTarget) (runtimeport.ByteStream, error) {
	ctx, span := observability.StartSpan(ctx, "runtime.kubevirt.open_vnc")
	defer span.End()
	if a.validator != nil && !target.Binding.Enabled {
		return nil, runtimeport.ErrInvalidTarget
	}
	return a.open(ctx, target, a.provider.OpenVNC)
}

func (a *Adapter) open(ctx context.Context, target runtimeport.VMTarget, connect func(string, string) (providerStream, error)) (runtimeport.ByteStream, error) {
	namespace, err := kubernetesruntime.NamespaceForTenant(target.TenantID)
	name := target.WorkloadName
	if b := target.Binding; b.Enabled {
		if !session.ValidVMBinding(b) || target.TenantID != b.TenantID || target.InstanceID != b.VMID || target.WorkloadName != b.VMName || a.validator == nil || a.clusterID == "" || a.clusterID != b.ClusterID || len(validation.IsDNS1123Label(b.Namespace)) != 0 || len(validation.IsDNS1123Subdomain(b.VMName)) != 0 || len(validation.IsDNS1123Subdomain(b.VMIName)) != 0 {
			return nil, runtimeport.ErrInvalidTarget
		}
		if err := a.validateManaged(ctx, b); err != nil {
			return nil, err
		}
		namespace, name, err = b.Namespace, b.VMIName, nil
	}
	if err != nil || name == "" || len(validation.IsDNS1123Subdomain(name)) != 0 || len(validation.IsDNS1123Label(namespace)) != 0 {
		return nil, runtimeport.ErrInvalidTarget
	}
	state, err := a.provider.Get(ctx, namespace, name)
	if err != nil {
		if apierrors.IsNotFound(err) {
			return nil, runtimeport.ErrTargetNotFound
		}
		return nil, runtimeport.ErrUnavailable
	}
	if !target.Binding.Enabled && (state.namespace != namespace || state.name != target.WorkloadName || state.labels[tenantLabel] != target.TenantID || state.labels[instanceLabel] != target.WorkloadName) {
		return nil, runtimeport.ErrTargetNotFound
	}
	if target.Binding.Enabled && !matchesVMI(state, target.Binding) {
		return nil, runtimeport.ErrTargetChanged
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
		stream, err := connect(namespace, name)
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
			disposeProviderStream(connected.stream)
			return nil, runtimeport.ErrUnavailable
		}
		if connectCtx.Err() != nil {
			disposeProviderStream(connected.stream)
			return nil, connectCtx.Err()
		}
		// The KubeVirt VNC subresource has no UID precondition. Re-observe the
		// frozen identity and business closing intent after upgrade, while the
		// provider is still private: no browser input or RFB bytes can pass yet.
		if target.Binding.Enabled {
			if err := a.validateManaged(connectCtx, target.Binding); err != nil {
				disposeProviderStream(connected.stream)
				return nil, err
			}
		}
		return newManagedStream(ctx, connected.stream), nil
	}
}

func matchesVMI(state vmiState, b session.VMBinding) bool {
	if state.apiVersion != "kubevirt.io/v1" || state.kind != "VirtualMachineInstance" || state.namespace != b.Namespace || state.name != b.VMIName || state.uid != b.VMIUID || state.deleting || state.phase != string(kubevirtv1.Running) {
		return false
	}
	for _, owner := range state.owners {
		if owner.APIVersion == "kubevirt.io/v1" && owner.Kind == "VirtualMachine" && owner.Name == b.VMName && string(owner.UID) == b.VMUID && owner.Controller != nil && *owner.Controller {
			return true
		}
	}
	return false
}

func (a *Adapter) validateManaged(ctx context.Context, b session.VMBinding) error {
	identity, ok := a.provider.(vmIdentityProvider)
	if !ok {
		return runtimeport.ErrUnavailable
	}
	if err := identity.ValidateVMIdentity(ctx, b); err != nil {
		return err
	}
	state, err := a.provider.Get(ctx, b.Namespace, b.VMIName)
	if err != nil {
		return identityReadError(err)
	}
	if !matchesVMI(state, b) {
		return runtimeport.ErrTargetChanged
	}
	// Resource performs native observation followed by a fresh business-row
	// read, so a concurrent Stop/Delete cannot be hidden by the first read.
	if err := a.validator.ValidateVM(ctx, b); err != nil {
		if errors.Is(err, session.ErrFailedPrecondition) {
			return runtimeport.ErrTargetChanged
		}
		return runtimeport.ErrUnavailable
	}
	return nil
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
	if conn := stream.AsConn(); conn != nil {
		_ = conn.Close()
	}
	_ = stream.Stream(kvcorev1.StreamOptions{In: bytes.NewReader(nil), Out: io.Discard})
}

var _ runtimeport.VMConsoleRuntime = (*Adapter)(nil)
var _ runtimeport.ByteStream = (*managedStream)(nil)
