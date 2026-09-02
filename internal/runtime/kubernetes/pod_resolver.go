package kubernetesruntime

import (
	"context"
	"fmt"
	"sort"

	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/labels"
	"k8s.io/apimachinery/pkg/util/validation"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
)

const (
	tenantLabel   = "ani.kubercloud.io/tenant-id"
	instanceLabel = "ani.kubercloud.io/instance"
)

type ResolvedPod struct{ Namespace, Name, Container string }

type podListClient interface {
	ListPods(context.Context, string, metav1.ListOptions) (*corev1.PodList, error)
}

type corePodListClient struct{ client typedcorev1.CoreV1Interface }

func (c corePodListClient) ListPods(ctx context.Context, namespace string, options metav1.ListOptions) (*corev1.PodList, error) {
	return c.client.Pods(namespace).List(ctx, options)
}

type PodResolver struct{ client podListClient }

func NewPodResolver(client typedcorev1.CoreV1Interface) *PodResolver {
	return &PodResolver{client: corePodListClient{client: client}}
}

func NamespaceForTenant(tenantID string) (string, error) {
	namespace := "ani-tenant-" + tenantID
	if tenantID == "" || len(validation.IsDNS1123Label(namespace)) != 0 {
		return "", runtimeport.ErrInvalidTarget
	}
	return namespace, nil
}

func (r *PodResolver) Resolve(ctx context.Context, target runtimeport.ExecTarget) (ResolvedPod, error) {
	if target.WorkloadKind != session.WorkloadContainer && target.WorkloadKind != session.WorkloadGPUContainer && target.WorkloadKind != session.WorkloadSandbox {
		return ResolvedPod{}, runtimeport.ErrInvalidTarget
	}
	namespace, err := NamespaceForTenant(target.TenantID)
	if err != nil {
		return ResolvedPod{}, err
	}
	selector := labels.Set{tenantLabel: target.TenantID, instanceLabel: target.WorkloadName}.AsSelector().String()
	pods, err := r.client.ListPods(ctx, namespace, metav1.ListOptions{LabelSelector: selector})
	if err != nil {
		return ResolvedPod{}, fmt.Errorf("%w: list eligible pods", runtimeport.ErrUnavailable)
	}
	eligible := make([]corev1.Pod, 0, len(pods.Items))
	matched := false
	for _, pod := range pods.Items {
		if pod.Labels[tenantLabel] != target.TenantID || pod.Labels[instanceLabel] != target.WorkloadName {
			continue
		}
		matched = true
		if pod.DeletionTimestamp == nil && pod.Status.Phase == corev1.PodRunning && podReady(pod.Status.Conditions) {
			eligible = append(eligible, pod)
		}
	}
	if len(eligible) == 0 {
		if !matched {
			return ResolvedPod{}, runtimeport.ErrTargetNotFound
		}
		return ResolvedPod{}, runtimeport.ErrTargetNotReady
	}
	sort.Slice(eligible, func(i, j int) bool {
		if eligible[i].CreationTimestamp.Time.Equal(eligible[j].CreationTimestamp.Time) {
			return eligible[i].Name < eligible[j].Name
		}
		return eligible[i].CreationTimestamp.After(eligible[j].CreationTimestamp.Time)
	})
	selected := eligible[0]
	container := target.Container
	if container == "" {
		if len(selected.Spec.Containers) != 1 {
			return ResolvedPod{}, runtimeport.ErrAmbiguousContainer
		}
		container = selected.Spec.Containers[0].Name
	} else {
		found := false
		for _, candidate := range selected.Spec.Containers {
			if candidate.Name == container {
				found = true
				break
			}
		}
		if !found {
			return ResolvedPod{}, runtimeport.ErrInvalidTarget
		}
	}
	return ResolvedPod{Namespace: namespace, Name: selected.Name, Container: container}, nil
}

func podReady(conditions []corev1.PodCondition) bool {
	for _, condition := range conditions {
		if condition.Type == corev1.PodReady && condition.Status == corev1.ConditionTrue {
			return true
		}
	}
	return false
}
