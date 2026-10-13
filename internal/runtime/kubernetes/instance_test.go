package kubernetesruntime

import (
	"context"
	"encoding/json"
	"errors"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"testing"
	"time"
)

func TestManagedInstanceResolverRejectsReplacementPod(t *testing.T) {
	p := pod("saved-tenant", "notebook-0", "tenant", "notebook", time.Now(), true, "workspace")
	p.UID = types.UID("pod-original")
	p.Labels = map[string]string{"accelerator.ani.io/resource-tenant-id": "tenant", "accelerator.ani.io/resource-id": "instance", "accelerator.ani.io/owner-service": "ani-resource-service"}
	r, client := resolverForPods(p)
	target := runtimeport.ExecTarget{InstanceID: "instance", TenantID: "tenant", WorkloadName: "notebook", WorkloadKind: session.WorkloadGPUContainer, Container: "workspace", InstanceBinding: session.InstanceBinding{Enabled: true, Namespace: "saved-tenant", PodName: "notebook-0", PodUID: "pod-original", ContainerName: "workspace"}}
	resolved, e := r.Resolve(context.Background(), target)
	if e != nil || resolved.Namespace != "saved-tenant" || client.namespace != "saved-tenant" {
		t.Fatal("saved Namespace not used", e)
	}
	client.pods.Items[0].UID = types.UID("replacement")
	if _, e = r.Resolve(context.Background(), target); !errors.Is(e, runtimeport.ErrTargetNotFound) {
		t.Fatal("old ticket connected to replacement", e)
	}
	client.pods.Items[0].UID = p.UID
	target.TenantID = "other"
	if _, e = r.Resolve(context.Background(), target); !errors.Is(e, runtimeport.ErrTargetNotFound) {
		t.Fatal("cross tenant exec accepted", e)
	}
}

type instanceAuthority struct{ denied bool }

func (a *instanceAuthority) Validate(context.Context, string, string, session.InstanceBinding) error {
	if a.denied {
		return session.ErrFailedPrecondition
	}
	return nil
}
func TestManagedInstanceExecGuardRejectsPodReplacement(t *testing.T) {
	p := pod("saved-tenant", "notebook-0", "tenant", "notebook", time.Now(), true, "workspace")
	p.UID = types.UID("pod-original")
	p.Labels = map[string]string{"accelerator.ani.io/resource-tenant-id": "tenant", "accelerator.ani.io/resource-id": "instance", "accelerator.ani.io/owner-service": "ani-resource-service"}
	api := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: []corev1.Pod{*p}})
	}))
	defer api.Close()
	config := &rest.Config{Host: api.URL}
	client, e := typedcorev1.NewForConfig(config)
	if e != nil {
		t.Fatal(e)
	}
	adapter := NewExecAdapter(config, client)
	authority := &instanceAuthority{}
	adapter.ConfigureInstanceValidator(authority, "saved-cluster")
	var command []string
	adapter.newExecutor = func(_ *rest.Config, _ string, u *url.URL) (remotecommand.Executor, error) {
		command = u.Query()["command"]
		return executorFunc(func(context.Context, remotecommand.StreamOptions) error { return nil }), nil
	}
	target := runtimeport.ExecTarget{InstanceID: "instance", TenantID: "tenant", WorkloadName: "notebook", WorkloadKind: session.WorkloadGPUContainer, Container: "workspace", Command: []string{"/bin/sh", "-c", "printf guarded-command-ran"}, InstanceBinding: session.InstanceBinding{Enabled: true, Version: 1, ClusterID: "saved-cluster", Namespace: p.Namespace, NotebookName: "notebook", NotebookUID: "notebook-original", PodName: p.Name, PodUID: string(p.UID), ContainerName: "workspace", Shell: "/bin/sh"}}
	stream, e := adapter.OpenExec(context.Background(), target, session.TerminalSize{})
	if e != nil {
		t.Fatal(e)
	}
	_ = stream.Close()
	_, _ = stream.Wait()
	if len(command) == 0 {
		t.Fatal("exec request missing command")
	}
	for _, uid := range []string{"pod-original", "replacement"} {
		cmd := exec.Command(command[0], command[1:]...)
		cmd.Env = append(os.Environ(), "ANI_INSTANCE_POD_UID="+uid)
		output, e := cmd.CombinedOutput()
		if uid == "pod-original" {
			if e != nil || string(output) != "guarded-command-ran" {
				t.Fatal("original Pod command failed", e)
			}
		} else {
			var exit *exec.ExitError
			if !errors.As(e, &exit) || exit.ExitCode() != 126 || len(output) != 0 {
				t.Fatal("replacement Pod executed command", e)
			}
		}
	}
	authority.denied = true
	if _, e = adapter.OpenExec(context.Background(), target, session.TerminalSize{}); !errors.Is(e, runtimeport.ErrTargetNotReady) {
		t.Fatal("revoked instance opened exec", e)
	}
}
