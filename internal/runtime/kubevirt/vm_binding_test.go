package kubevirtruntime

import (
	"context"
	"errors"
	"io"
	"testing"
	"time"

	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
)

type managedProvider struct {
	*fakeProvider
	binding     session.VMBinding
	rootChanged bool
	onOpen      func()
}

func (p *managedProvider) ValidateVMIdentity(_ context.Context, binding session.VMBinding) error {
	if p.rootChanged || binding.NamespaceUID != p.binding.NamespaceUID || binding.VMUID != p.binding.VMUID {
		return runtimeport.ErrTargetChanged
	}
	return nil
}

func (p *managedProvider) OpenVNC(namespace, name string) (providerStream, error) {
	stream, err := p.fakeProvider.OpenVNC(namespace, name)
	if p.onOpen != nil {
		p.onOpen()
	}
	return stream, err
}

type managedAuthority struct {
	calls          int
	closeAfterOpen bool
}

func (a *managedAuthority) ValidateVM(context.Context, session.VMBinding) error {
	a.calls++
	if a.closeAfterOpen && a.calls > 1 {
		return session.ErrFailedPrecondition
	}
	return nil
}

func managedFixture() (*managedProvider, runtimeport.VMTarget) {
	b := session.VMBinding{Enabled: true, TenantID: "tenant-a", VMID: "vm-id", Version: 4, ClusterID: "cluster", Namespace: "frozen-tenant", NamespaceUID: "namespace-uid", VMName: "vm-a", VMUID: "vm-uid", VMIName: "vm-a", VMIUID: "vmi-uid"}
	controller := true
	p := &managedProvider{fakeProvider: &fakeProvider{state: vmiState{namespace: b.Namespace, name: b.VMIName, apiVersion: "kubevirt.io/v1", kind: "VirtualMachineInstance", uid: b.VMIUID, phase: "Running", owners: []metav1.OwnerReference{{APIVersion: "kubevirt.io/v1", Kind: "VirtualMachine", Name: b.VMName, UID: types.UID(b.VMUID), Controller: &controller}}}}, binding: b}
	return p, runtimeport.VMTarget{TenantID: b.TenantID, InstanceID: b.VMID, WorkloadName: b.VMName, Binding: b}
}

func TestManagedVNCUsesFrozenNamespaceAndPreservesBytesWithoutGuestReadiness(t *testing.T) {
	p, target := managedFixture()
	authority := &managedAuthority{}
	a := &Adapter{provider: p, connectionTimeout: time.Second}
	a.ConfigureVMValidator(authority, "cluster")
	stream, err := a.OpenVNC(t.Context(), target)
	if err != nil {
		t.Fatal(err)
	}
	defer stream.Close()
	defer p.peer.Close()
	if p.openedNS != target.Binding.Namespace || p.openedName != target.Binding.VMIName || p.openedMode != "vnc" || authority.calls != 2 {
		t.Fatalf("wrong native target/validation count: namespace=%s name=%s mode=%s calls=%d", p.openedNS, p.openedName, p.openedMode, authority.calls)
	}
	// This VMI has no guest IP, Agent or installation status. Native VNC is
	// usable for installation and the RFB bytes remain opaque to the gateway.
	payload := []byte{0, 'R', 'F', 'B', 0xff}
	go func() { _, _ = p.peer.Write(payload) }()
	got := make([]byte, len(payload))
	if _, err := io.ReadFull(stream, got); err != nil || string(got) != string(payload) {
		t.Fatalf("RFB changed: %v", err)
	}
}

func TestManagedVNCRejectsWrongUIDAndOwnerBeforeOpening(t *testing.T) {
	for name, mutate := range map[string]func(*managedProvider, *runtimeport.VMTarget){
		"wrong VMI kind":      func(p *managedProvider, _ *runtimeport.VMTarget) { p.state.kind = "VirtualMachine" },
		"wrong VMI version":   func(p *managedProvider, _ *runtimeport.VMTarget) { p.state.apiVersion = "other/v1" },
		"same name new VMI":   func(p *managedProvider, _ *runtimeport.VMTarget) { p.state.uid = "replacement" },
		"same name new VM":    func(p *managedProvider, _ *runtimeport.VMTarget) { p.rootChanged = true },
		"namespace replaced":  func(_ *managedProvider, target *runtimeport.VMTarget) { target.Binding.NamespaceUID = "replacement" },
		"wrong owner kind":    func(p *managedProvider, _ *runtimeport.VMTarget) { p.state.owners[0].Kind = "Pod" },
		"wrong owner version": func(p *managedProvider, _ *runtimeport.VMTarget) { p.state.owners[0].APIVersion = "other/v1" },
		"wrong owner UID":     func(p *managedProvider, _ *runtimeport.VMTarget) { p.state.owners[0].UID = "replacement" },
		"wrong owner name":    func(p *managedProvider, _ *runtimeport.VMTarget) { p.state.owners[0].Name = "other" },
		"not controller":      func(p *managedProvider, _ *runtimeport.VMTarget) { p.state.owners[0].Controller = nil },
		"not running":         func(p *managedProvider, _ *runtimeport.VMTarget) { p.state.phase = "Pending" },
		"deleting":            func(p *managedProvider, _ *runtimeport.VMTarget) { p.state.deleting = true },
	} {
		t.Run(name, func(t *testing.T) {
			p, target := managedFixture()
			mutate(p, &target)
			a := &Adapter{provider: p, connectionTimeout: time.Second}
			a.ConfigureVMValidator(&managedAuthority{}, "cluster")
			if _, err := a.OpenVNC(t.Context(), target); !errors.Is(err, runtimeport.ErrTargetChanged) {
				t.Fatalf("changed target error=%v", err)
			}
			if p.openedMode != "" {
				t.Fatal("identity check happened after native open")
			}
		})
	}
}

func TestManagedVNCRaceClosesNativeStreamBeforeBrowserForwarding(t *testing.T) {
	for _, race := range []string{"vmi replaced", "VM replaced", "business closing"} {
		t.Run(race, func(t *testing.T) {
			p, target := managedFixture()
			authority := &managedAuthority{closeAfterOpen: race == "business closing"}
			p.onOpen = func() {
				if race == "vmi replaced" {
					p.state.uid = "replacement"
				}
				if race == "VM replaced" {
					p.rootChanged = true
				}
			}
			a := &Adapter{provider: p, connectionTimeout: time.Second}
			a.ConfigureVMValidator(authority, "cluster")
			stream, err := a.OpenVNC(t.Context(), target)
			if stream != nil || !errors.Is(err, runtimeport.ErrTargetChanged) {
				t.Fatalf("changed target escaped: stream=%v err=%v", stream != nil, err)
			}
			defer p.peer.Close()
			_ = p.peer.SetReadDeadline(time.Now().Add(time.Second))
			if _, err := p.peer.Read(make([]byte, 1)); !errors.Is(err, io.EOF) {
				t.Fatalf("wrong native stream remained open: %v", err)
			}
		})
	}
}

func TestManagedVNCRejectsWrongClusterSerialAndNameOnlyFallback(t *testing.T) {
	p, target := managedFixture()
	a := &Adapter{provider: p, connectionTimeout: time.Second}
	a.ConfigureVMValidator(&managedAuthority{}, "another-cluster")
	if _, err := a.OpenVNC(t.Context(), target); !errors.Is(err, runtimeport.ErrInvalidTarget) {
		t.Fatal(err)
	}
	if _, err := a.OpenSerial(t.Context(), target); !errors.Is(err, runtimeport.ErrInvalidTarget) {
		t.Fatal(err)
	}
	target.Binding = session.VMBinding{}
	if _, err := a.OpenVNC(t.Context(), target); !errors.Is(err, runtimeport.ErrInvalidTarget) {
		t.Fatal(err)
	}
}
