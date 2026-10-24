package kubevirtruntime

import (
	"encoding/json"
	"encoding/pem"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"k8s.io/client-go/rest"
)

// Exercise the actual REST client's response decoder. A fake provider with
// hand-populated TypeMeta cannot detect loss of kind/apiVersion during decode.
func TestManagedVMIdentityPreservesAndChecksWireGVK(t *testing.T) {
	for _, scenario := range []struct {
		name, vmVersion, vmKind, vmiVersion, vmiKind string
		changed                                      bool
	}{
		{"valid wire identity", "kubevirt.io/v1", "VirtualMachine", "kubevirt.io/v1", "VirtualMachineInstance", false},
		{"wrong VM kind", "kubevirt.io/v1", "VirtualMachineInstance", "kubevirt.io/v1", "VirtualMachineInstance", true},
		{"wrong VM API version", "other/v1", "VirtualMachine", "kubevirt.io/v1", "VirtualMachineInstance", true},
		{"wrong VMI kind", "kubevirt.io/v1", "VirtualMachine", "kubevirt.io/v1", "VirtualMachine", true},
		{"wrong VMI API version", "kubevirt.io/v1", "VirtualMachine", "other/v1", "VirtualMachineInstance", true},
	} {
		t.Run(scenario.name, func(t *testing.T) {
			_, target := managedFixture()
			b := target.Binding
			namespace := map[string]any{"apiVersion": "v1", "kind": "Namespace", "metadata": map[string]any{"name": b.Namespace, "uid": b.NamespaceUID}}
			vm := map[string]any{"apiVersion": scenario.vmVersion, "kind": scenario.vmKind, "metadata": map[string]any{"name": b.VMName, "namespace": b.Namespace, "uid": b.VMUID}}
			vmi := map[string]any{"apiVersion": scenario.vmiVersion, "kind": scenario.vmiKind, "metadata": map[string]any{"name": b.VMIName, "namespace": b.Namespace, "uid": b.VMIUID,
				"ownerReferences": []any{map[string]any{"apiVersion": "kubevirt.io/v1", "kind": "VirtualMachine", "name": b.VMName, "uid": b.VMUID, "controller": true}}}, "status": map[string]any{"phase": "Running"}}
			server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Content-Type", "application/json")
				var response any
				switch request.URL.Path {
				case "/api/v1/namespaces/" + b.Namespace:
					response = namespace
				case "/apis/kubevirt.io/v1/namespaces/" + b.Namespace + "/virtualmachines/" + b.VMName:
					response = vm
				case "/apis/kubevirt.io/v1/namespaces/" + b.Namespace + "/virtualmachineinstances/" + b.VMIName:
					response = vmi
				default:
					t.Errorf("unexpected identity request: %s %s", request.Method, request.URL.Path)
					http.NotFound(w, request)
					return
				}
				if request.Method != http.MethodGet {
					t.Errorf("identity verification made a non-read request: %s", request.Method)
				}
				_ = json.NewEncoder(w).Encode(response)
			}))
			defer server.Close()
			ca := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: server.Certificate().Raw})
			provider, err := newKubeVirtProvider(&rest.Config{Host: server.URL, TLSClientConfig: rest.TLSClientConfig{CAData: ca}})
			if err != nil {
				t.Fatal(err)
			}
			authority := &managedAuthority{}
			adapter := &Adapter{provider: provider, connectionTimeout: time.Second, validator: authority}
			err = adapter.validateManaged(t.Context(), b)
			if scenario.changed {
				if !errors.Is(err, runtimeport.ErrTargetChanged) || authority.calls != 0 {
					t.Fatalf("wrong wire GVK accepted: error=%v authorityCalls=%d", err, authority.calls)
				}
				return
			}
			if err != nil || authority.calls != 1 {
				t.Fatalf("valid wire identity rejected: error=%v authorityCalls=%d", err, authority.calls)
			}
			state, err := provider.Get(t.Context(), b.Namespace, b.VMIName)
			if err != nil || state.apiVersion != "kubevirt.io/v1" || state.kind != "VirtualMachineInstance" {
				t.Fatalf("wire GVK lost: state=%+v error=%v", state, err)
			}
		})
	}
}
