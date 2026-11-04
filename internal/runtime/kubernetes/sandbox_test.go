package kubernetesruntime

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"

	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

type sandboxAuthority struct {
	denied bool
	calls  int
	denyAt int
}

func (a *sandboxAuthority) ValidateSandbox(context.Context, session.SandboxBinding) error {
	a.calls++
	if a.denied || (a.denyAt > 0 && a.calls >= a.denyAt) {
		return session.ErrFailedPrecondition
	}
	return nil
}
func sandboxFixture() (*corev1.Pod, runtimeport.ExecTarget) {
	tenant := "9dfd5edc-e859-4890-a0a5-9dc610a80ea5"
	id := "17fb261b-413b-463f-a194-798c5ee60fb8"
	ns := "tenant-" + tenant
	name := "sandbox-" + id
	p := pod(ns, name, tenant, "unused", time.Now(), true, "sandbox")
	p.UID = types.UID("pod-uid")
	p.Labels = map[string]string{"ani.io/tenant-id": tenant, "ani.io/sandbox-owner": id, "ani.io/owner-service": "ani-resource-service"}
	return p, runtimeport.ExecTarget{SandboxID: id, TenantID: tenant, WorkloadName: name, WorkloadKind: session.WorkloadSandbox, Container: "sandbox", Command: []string{"/bin/sh", "-c", "printf sandbox-command-ran"}, SandboxBinding: session.SandboxBinding{Enabled: true, TenantID: tenant, SandboxID: id, Version: 4, ClusterID: "cluster", Namespace: ns, NamespaceUID: "ns-uid", PodName: name, PodUID: "pod-uid", ContainerName: "sandbox", Shell: "/bin/sh"}}
}
func TestSandboxPodResolverRejectsReplacementAndWrongTenant(t *testing.T) {
	p, target := sandboxFixture()
	r, client := resolverForPods(p)
	got, err := r.Resolve(t.Context(), target)
	if err != nil || got.Name != p.Name {
		t.Fatalf("valid: %+v %v", got, err)
	}
	client.pods.Items[0].UID = "replacement"
	if _, err = r.Resolve(t.Context(), target); !errors.Is(err, runtimeport.ErrTargetNotFound) {
		t.Fatalf("replacement: %v", err)
	}
	client.pods.Items[0].UID = p.UID
	client.pods.Items[0].Labels["ani.io/tenant-id"] = "another"
	if _, err = r.Resolve(t.Context(), target); !errors.Is(err, runtimeport.ErrTargetNotFound) {
		t.Fatalf("tenant: %v", err)
	}
}
func TestSandboxExecGuardAndNamespaceReplacement(t *testing.T) {
	p, target := sandboxFixture()
	namespaceUID := "ns-uid"
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/namespaces/"+p.Namespace {
			_ = json.NewEncoder(w).Encode(&corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: p.Namespace, UID: types.UID(namespaceUID)}})
			return
		}
		_ = json.NewEncoder(w).Encode(&corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []corev1.Pod{*p}})
	}))
	defer api.Close()
	cfg := &rest.Config{Host: api.URL}
	client, err := typedcorev1.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := NewExecAdapter(cfg, client, time.Second)
	authority := &sandboxAuthority{}
	a.ConfigureInstanceValidator(nil, "cluster")
	a.ConfigureSandboxValidator(authority)
	var command []string
	a.newExecutor = func(_ *rest.Config, _ string, u *url.URL) (remotecommand.Executor, error) {
		command = u.Query()["command"]
		return executorFunc(func(context.Context, remotecommand.StreamOptions) error { return nil }), nil
	}
	stream, err := a.OpenExec(t.Context(), target, session.TerminalSize{})
	if err != nil {
		t.Fatal(err)
	}
	_ = stream.Close()
	_, _ = stream.Wait()
	for _, uid := range []string{"pod-uid", "replacement", ""} {
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Env = append(os.Environ(), "ANI_SANDBOX_POD_UID="+uid)
		out, err := cmd.CombinedOutput()
		if uid == "pod-uid" {
			if err != nil || string(out) != "sandbox-command-ran" {
				t.Fatalf("original: %s %v", out, err)
			}
		} else {
			var exit *exec.ExitError
			if !errors.As(err, &exit) || exit.ExitCode() != 125 || len(out) != 0 {
				t.Fatalf("replacement guard: %q %v", out, err)
			}
		}
	}
	namespaceUID = "replacement"
	if _, err = a.OpenExec(t.Context(), target, session.TerminalSize{}); !errors.Is(err, runtimeport.ErrTargetChanged) {
		t.Fatalf("namespace replacement: %v", err)
	}
	namespaceUID = "ns-uid"
	authority.denied = true
	if _, err = a.OpenExec(t.Context(), target, session.TerminalSize{}); !errors.Is(err, runtimeport.ErrTargetNotReady) {
		t.Fatalf("revocation: %v", err)
	}
}

// A pause/delete accepted during Namespace/Pod resolution is rejected by the
// second Resource validation. A transition after that validation can still
// race Kubernetes exec; UID guarding does not serialize business operations.
func TestSandboxTransitionDuringPreflightBlocksExec(t *testing.T) {
	p, target := sandboxFixture()
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		if r.URL.Path == "/api/v1/namespaces/"+p.Namespace {
			_ = json.NewEncoder(w).Encode(&corev1.Namespace{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "Namespace"}, ObjectMeta: metav1.ObjectMeta{Name: p.Namespace, UID: types.UID("ns-uid")}})
			return
		}
		_ = json.NewEncoder(w).Encode(&corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []corev1.Pod{*p}})
	}))
	defer api.Close()
	cfg := &rest.Config{Host: api.URL}
	client, err := typedcorev1.NewForConfig(cfg)
	if err != nil {
		t.Fatal(err)
	}
	a := NewExecAdapter(cfg, client, time.Second)
	authority := &sandboxAuthority{denyAt: 2}
	a.ConfigureInstanceValidator(nil, "cluster")
	a.ConfigureSandboxValidator(authority)
	executorCalled := false
	a.newExecutor = func(_ *rest.Config, _ string, _ *url.URL) (remotecommand.Executor, error) {
		executorCalled = true
		return nil, nil
	}
	if _, err := a.OpenExec(t.Context(), target, session.TerminalSize{}); !errors.Is(err, runtimeport.ErrTargetNotReady) || executorCalled || authority.calls != 2 {
		t.Fatalf("state changed during preflight: executor=%t checks=%d error=%v", executorCalled, authority.calls, err)
	}
}
