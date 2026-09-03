package deployment

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	rbacv1 "k8s.io/api/rbac/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

type manifests struct {
	deployment *appsv1.Deployment
	services   map[string]*corev1.Service
	account    *corev1.ServiceAccount
	config     *corev1.ConfigMap
	role       *rbacv1.ClusterRole
	binding    *rbacv1.ClusterRoleBinding
	policy     *networkingv1.NetworkPolicy
}

func TestBaseManifestsAreStrictAndComplete(t *testing.T) {
	base := manifestBase(t)
	got := loadManifests(t, base)
	assertDeployment(t, got.deployment)
	assertServices(t, got.services)
	assertRBAC(t, got.role, got.binding)
	assertNetworkPolicy(t, got.policy)
	if got.account == nil || got.account.Name != "ani-session-gateway" || got.account.AutomountServiceAccountToken == nil || !*got.account.AutomountServiceAccountToken {
		t.Fatal("dedicated ServiceAccount or token mount is missing")
	}
	if got.config == nil || got.config.Data["STORE_MODE"] != "redis" || got.config.Data["PUBLIC_WS_BASE_URL"] == "" || strings.Contains(got.config.Data["ALLOWED_ORIGINS"], "*") {
		t.Fatal("fail-fast deployment configuration is incomplete")
	}
	assertKustomization(t, base)
}

func manifestBase(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("resolve test path")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "deploy", "base"))
}

func loadManifests(t *testing.T, base string) manifests {
	t.Helper()
	result := manifests{services: map[string]*corev1.Service{}}
	files, err := filepath.Glob(filepath.Join(base, "*.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	count := 0
	for _, file := range files {
		if filepath.Base(file) == "kustomization.yaml" {
			continue
		}
		raw, err := os.ReadFile(file)
		if err != nil {
			t.Fatal(err)
		}
		decoder := utilyaml.NewYAMLToJSONDecoder(bytes.NewReader(raw))
		for {
			var document json.RawMessage
			if err := decoder.Decode(&document); errorsIsEOF(err) {
				break
			} else if err != nil {
				t.Fatalf("decode %s: %v", file, err)
			}
			if len(document) == 0 {
				continue
			}
			var meta struct {
				Kind     string            `json:"kind"`
				Metadata metav1.ObjectMeta `json:"metadata"`
			}
			if err := json.Unmarshal(document, &meta); err != nil {
				t.Fatalf("metadata %s: %v", file, err)
			}
			count++
			switch meta.Kind {
			case "Deployment":
				result.deployment = decodeStrict[appsv1.Deployment](t, file, document)
			case "Service":
				result.services[meta.Metadata.Name] = decodeStrict[corev1.Service](t, file, document)
			case "ServiceAccount":
				result.account = decodeStrict[corev1.ServiceAccount](t, file, document)
			case "ConfigMap":
				result.config = decodeStrict[corev1.ConfigMap](t, file, document)
			case "ClusterRole":
				result.role = decodeStrict[rbacv1.ClusterRole](t, file, document)
			case "ClusterRoleBinding":
				result.binding = decodeStrict[rbacv1.ClusterRoleBinding](t, file, document)
			case "NetworkPolicy":
				result.policy = decodeStrict[networkingv1.NetworkPolicy](t, file, document)
			default:
				t.Fatalf("unexpected kind %q in %s", meta.Kind, file)
			}
		}
	}
	if count != 8 || result.deployment == nil || len(result.services) != 2 || result.role == nil || result.binding == nil || result.policy == nil {
		t.Fatalf("manifest inventory incomplete: documents=%d services=%d", count, len(result.services))
	}
	return result
}

func errorsIsEOF(err error) bool { return err == io.EOF }

func decodeStrict[T any](t *testing.T, file string, raw []byte) *T {
	t.Helper()
	var result T
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&result); err != nil {
		t.Fatalf("strict decode %s: %v", file, err)
	}
	return &result
}

func assertDeployment(t *testing.T, deployment *appsv1.Deployment) {
	t.Helper()
	if deployment == nil || deployment.Spec.Replicas == nil || *deployment.Spec.Replicas < 1 || deployment.Spec.Template.Spec.ServiceAccountName != "ani-session-gateway" {
		t.Fatal("Deployment replicas or ServiceAccount is invalid")
	}
	pod := deployment.Spec.Template.Spec
	if pod.TerminationGracePeriodSeconds == nil || *pod.TerminationGracePeriodSeconds <= 25 || pod.SecurityContext == nil || pod.SecurityContext.RunAsNonRoot == nil || !*pod.SecurityContext.RunAsNonRoot || pod.SecurityContext.RunAsUser == nil || *pod.SecurityContext.RunAsUser != 65532 || pod.SecurityContext.RunAsGroup == nil || *pod.SecurityContext.RunAsGroup != 65532 || pod.SecurityContext.FSGroup == nil || *pod.SecurityContext.FSGroup != 65532 || pod.SecurityContext.FSGroupChangePolicy == nil || *pod.SecurityContext.FSGroupChangePolicy != corev1.FSGroupChangeOnRootMismatch || pod.SecurityContext.SeccompProfile == nil || pod.SecurityContext.SeccompProfile.Type != corev1.SeccompProfileTypeRuntimeDefault {
		t.Fatal("Pod graceful shutdown or security context is incomplete")
	}
	if len(pod.Containers) != 1 {
		t.Fatal("Deployment must have one application container")
	}
	container := pod.Containers[0]
	if container.SecurityContext == nil || container.SecurityContext.AllowPrivilegeEscalation == nil || *container.SecurityContext.AllowPrivilegeEscalation || container.SecurityContext.ReadOnlyRootFilesystem == nil || !*container.SecurityContext.ReadOnlyRootFilesystem || container.SecurityContext.Capabilities == nil || len(container.SecurityContext.Capabilities.Drop) != 1 || container.SecurityContext.Capabilities.Drop[0] != "ALL" {
		t.Fatal("container security context is incomplete")
	}
	if container.Resources.Requests.Cpu().IsZero() || container.Resources.Requests.Memory().IsZero() || container.Resources.Limits.Cpu().IsZero() || container.Resources.Limits.Memory().IsZero() {
		t.Fatal("resource requests and limits are required")
	}
	if container.ReadinessProbe == nil || container.ReadinessProbe.HTTPGet == nil || container.ReadinessProbe.HTTPGet.Path != "/readyz" || container.LivenessProbe == nil || container.LivenessProbe.HTTPGet == nil || container.LivenessProbe.HTTPGet.Path != "/healthz" {
		t.Fatal("readiness/liveness probes are incomplete")
	}
	ports := map[string]int32{}
	for _, port := range container.Ports {
		if port.HostPort != 0 {
			t.Fatal("hostPort is forbidden")
		}
		ports[port.Name] = port.ContainerPort
	}
	if ports["http"] != 8080 || ports["grpc"] != 9090 {
		t.Fatalf("container ports=%v", ports)
	}
	var redisSecret, ticketPath, readOnlyMount bool
	for _, env := range container.Env {
		if env.Name == "REDIS_URL" && env.ValueFrom != nil && env.ValueFrom.SecretKeyRef != nil && env.ValueFrom.SecretKeyRef.Name == "ani-session-gateway-secrets" && env.ValueFrom.SecretKeyRef.Key == "redis-url" {
			redisSecret = true
		}
		if env.Name == "TICKET_ENCRYPTION_KEY_FILE" && env.Value == "/var/run/secrets/ani-session-gateway/ticket.key" {
			ticketPath = true
		}
	}
	for _, mount := range container.VolumeMounts {
		if mount.Name == "ticket-key" && mount.ReadOnly && mount.MountPath == "/var/run/secrets/ani-session-gateway" {
			readOnlyMount = true
		}
	}
	var ticketSecret bool
	for _, volume := range pod.Volumes {
		if volume.Name == "ticket-key" && volume.Secret != nil && volume.Secret.SecretName == "ani-session-gateway-secrets" && volume.Secret.DefaultMode != nil && *volume.Secret.DefaultMode == 0o440 && len(volume.Secret.Items) == 1 && volume.Secret.Items[0].Key == "ticket-encryption-key" && volume.Secret.Items[0].Path == "ticket.key" {
			ticketSecret = true
		}
	}
	if !redisSecret || !ticketPath || !readOnlyMount || !ticketSecret {
		t.Fatal("Redis Secret or exact read-only ticket-key mount is incomplete")
	}
}

func assertServices(t *testing.T, services map[string]*corev1.Service) {
	t.Helper()
	grpc := services["ani-session-gateway-grpc"]
	if grpc == nil || grpc.Spec.Type != corev1.ServiceTypeClusterIP || len(grpc.Spec.Ports) != 1 || grpc.Spec.Ports[0].Port != 9090 || grpc.Spec.Ports[0].NodePort != 0 {
		t.Fatal("gRPC must be ClusterIP-only on port 9090")
	}
	websocket := services["ani-session-gateway-websocket"]
	if websocket == nil || websocket.Spec.Type != corev1.ServiceTypeNodePort || len(websocket.Spec.Ports) != 1 || websocket.Spec.Ports[0].Port != 8080 || websocket.Spec.Ports[0].NodePort != 30082 {
		t.Fatal("WebSocket NodePort 30082 is incomplete")
	}
}

func assertRBAC(t *testing.T, role *rbacv1.ClusterRole, binding *rbacv1.ClusterRoleBinding) {
	t.Helper()
	want := []string{
		"|pods|get,list",
		"|pods/exec|create",
		"kubevirt.io|virtualmachineinstances|get",
		"subresources.kubevirt.io|virtualmachineinstances/console,virtualmachineinstances/vnc|get",
	}
	got := make([]string, 0, len(role.Rules))
	for _, rule := range role.Rules {
		if len(rule.APIGroups) != 1 || strings.Contains(strings.Join(rule.Resources, ","), "*") || strings.Contains(strings.Join(rule.Verbs, ","), "*") {
			t.Fatal("RBAC wildcard or ambiguous API group is forbidden")
		}
		sort.Strings(rule.Resources)
		sort.Strings(rule.Verbs)
		got = append(got, fmt.Sprintf("%s|%s|%s", rule.APIGroups[0], strings.Join(rule.Resources, ","), strings.Join(rule.Verbs, ",")))
	}
	sort.Strings(got)
	sort.Strings(want)
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("RBAC rules=%v", got)
	}
	if binding.RoleRef.Kind != "ClusterRole" || binding.RoleRef.Name != "ani-session-gateway" || len(binding.Subjects) != 1 || binding.Subjects[0].Kind != "ServiceAccount" || binding.Subjects[0].Name != "ani-session-gateway" || binding.Subjects[0].Namespace != "ani-system" {
		t.Fatal("ClusterRoleBinding target is invalid")
	}
}

func assertNetworkPolicy(t *testing.T, policy *networkingv1.NetworkPolicy) {
	t.Helper()
	if policy == nil || policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"] != "ani-session-gateway" || len(policy.Spec.PolicyTypes) != 2 {
		t.Fatal("NetworkPolicy selection/default deny is incomplete")
	}
	grpcRestricted, httpExposed := false, false
	for _, ingress := range policy.Spec.Ingress {
		for _, port := range ingress.Ports {
			if port.Port == nil {
				continue
			}
			switch port.Port.IntVal {
			case 8080:
				httpExposed = len(ingress.From) == 0
			case 9090:
				if len(ingress.From) == 1 && ingress.From[0].NamespaceSelector != nil && ingress.From[0].PodSelector != nil && ingress.From[0].NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] == "ani-system" && ingress.From[0].PodSelector.MatchLabels["app.kubernetes.io/name"] == "ani-gateway" {
					grpcRestricted = true
				}
			}
		}
	}
	if !grpcRestricted || !httpExposed {
		t.Fatal("ingress must expose 8080 and double-select only ani-gateway for 9090")
	}
	dns, redis, apiService, apiEndpoint, virtAPI := false, false, false, false, false
	for _, egress := range policy.Spec.Egress {
		for _, peer := range egress.To {
			if peer.NamespaceSelector != nil && peer.PodSelector != nil {
				namespace := peer.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"]
				if namespace == "kube-system" && peer.PodSelector.MatchLabels["k8s-app"] == "kube-dns" && hasEgressPort(egress, 53, corev1.ProtocolUDP) && hasEgressPort(egress, 53, corev1.ProtocolTCP) {
					dns = true
				}
				if namespace == "ani-system" && peer.PodSelector.MatchLabels["app"] == "ani-reconcile-ha-redis" && hasEgressPort(egress, 6379, corev1.ProtocolTCP) {
					redis = true
				}
				if namespace == "kubevirt" && peer.PodSelector.MatchLabels["kubevirt.io"] == "virt-api" && hasEgressPort(egress, 8443, corev1.ProtocolTCP) {
					virtAPI = true
				}
			}
			if peer.IPBlock != nil && peer.IPBlock.CIDR == "10.96.0.1/32" && hasEgressPort(egress, 443, corev1.ProtocolTCP) {
				apiService = true
			}
			if peer.IPBlock != nil && peer.IPBlock.CIDR == "10.10.1.66/32" && hasEgressPort(egress, 6443, corev1.ProtocolTCP) {
				apiEndpoint = true
			}
		}
	}
	if !dns || !redis || !apiService || !apiEndpoint || !virtAPI {
		t.Fatalf("egress selectors incomplete: dns=%v redis=%v api-service=%v api-endpoint=%v virt-api=%v", dns, redis, apiService, apiEndpoint, virtAPI)
	}
}

func hasEgressPort(rule networkingv1.NetworkPolicyEgressRule, number int32, protocol corev1.Protocol) bool {
	for _, port := range rule.Ports {
		if port.Port != nil && port.Port.IntVal == number && port.Protocol != nil && *port.Protocol == protocol {
			return true
		}
	}
	return false
}

func assertKustomization(t *testing.T, base string) {
	t.Helper()
	raw, err := os.ReadFile(filepath.Join(base, "kustomization.yaml"))
	if err != nil {
		t.Fatal(err)
	}
	for _, resource := range []string{"service-account.yaml", "rbac.yaml", "config-map.yaml", "deployment.yaml", "service-grpc.yaml", "service-websocket.yaml", "network-policy.yaml"} {
		if !bytes.Contains(raw, []byte("- "+resource)) {
			t.Fatalf("kustomization omits %s", resource)
		}
	}
}
