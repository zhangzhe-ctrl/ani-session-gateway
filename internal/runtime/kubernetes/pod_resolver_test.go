package kubernetesruntime

import (
	"context"
	"errors"
	"testing"
	"time"

	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
)

type fakePodListClient struct {
	pods      *corev1.PodList
	err       error
	namespace string
	options   metav1.ListOptions
}

func (f *fakePodListClient) ListPods(_ context.Context, namespace string, options metav1.ListOptions) (*corev1.PodList, error) {
	f.namespace = namespace
	f.options = options
	return f.pods.DeepCopy(), f.err
}

func resolverForPods(pods ...*corev1.Pod) (*PodResolver, *fakePodListClient) {
	items := make([]corev1.Pod, 0, len(pods))
	for _, candidate := range pods {
		items = append(items, *candidate.DeepCopy())
	}
	client := &fakePodListClient{pods: &corev1.PodList{Items: items}}
	return &PodResolver{client: client}, client
}

func TestPodResolverUsesBothLabelsAndNewestReadyPod(t *testing.T) {
	now := time.Now()
	resolver, client := resolverForPods(
		pod("ani-tenant-tenant-a", "old", "tenant-a", "workload-a", now.Add(-time.Minute), true, "app"),
		pod("ani-tenant-tenant-a", "new", "tenant-a", "workload-a", now, true, "app"),
		pod("ani-tenant-tenant-a", "cross-tenant", "tenant-b", "workload-a", now.Add(time.Minute), true, "app"),
	)
	resolved, err := resolver.Resolve(context.Background(), runtimeport.ExecTarget{TenantID: "tenant-a", WorkloadName: "workload-a", WorkloadKind: session.WorkloadContainer})
	if err != nil {
		t.Fatal(err)
	}
	if resolved.Name != "new" || resolved.Container != "app" || resolved.Namespace != "ani-tenant-tenant-a" {
		t.Fatalf("unexpected resolved Pod: %#v", resolved)
	}
	if client.namespace != "ani-tenant-tenant-a" || client.options.LabelSelector == "" {
		t.Fatalf("namespace=%q selector=%q", client.namespace, client.options.LabelSelector)
	}
}

func TestPodResolverFailsClosed(t *testing.T) {
	base := pod("ani-tenant-tenant-a", "pod", "tenant-a", "workload-a", time.Now(), false, "app")
	resolver, _ := resolverForPods(base)
	target := runtimeport.ExecTarget{TenantID: "tenant-a", WorkloadName: "workload-a", WorkloadKind: session.WorkloadContainer}
	if _, err := resolver.Resolve(context.Background(), target); !errors.Is(err, runtimeport.ErrTargetNotReady) {
		t.Fatalf("non-ready Pod error=%v", err)
	}
	base.Status.Conditions[0].Status = corev1.ConditionTrue
	base.Spec.Containers = append(base.Spec.Containers, corev1.Container{Name: "sidecar"})
	resolver, _ = resolverForPods(base)
	if _, err := resolver.Resolve(context.Background(), target); !errors.Is(err, runtimeport.ErrAmbiguousContainer) {
		t.Fatalf("ambiguous container error=%v", err)
	}
	target.Container = "missing"
	if _, err := resolver.Resolve(context.Background(), target); !errors.Is(err, runtimeport.ErrInvalidTarget) {
		t.Fatalf("missing container error=%v", err)
	}
	target.TenantID = "INVALID_TENANT"
	if _, err := resolver.Resolve(context.Background(), target); !errors.Is(err, runtimeport.ErrInvalidTarget) {
		t.Fatalf("invalid tenant error=%v", err)
	}
}

func pod(namespace, name, tenant, workload string, created time.Time, ready bool, containers ...string) *corev1.Pod {
	status := corev1.ConditionFalse
	if ready {
		status = corev1.ConditionTrue
	}
	podContainers := make([]corev1.Container, 0, len(containers))
	for _, container := range containers {
		podContainers = append(podContainers, corev1.Container{Name: container})
	}
	return &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Namespace: namespace, Name: name, Labels: map[string]string{tenantLabel: tenant, instanceLabel: workload}, CreationTimestamp: metav1.NewTime(created)}, Spec: corev1.PodSpec{Containers: podContainers}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: status}}}}
}
