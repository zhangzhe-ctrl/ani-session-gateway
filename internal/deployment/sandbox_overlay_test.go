package deployment

import (
	"bytes"
	"encoding/json"
	"io"
	"os/exec"
	"path/filepath"
	"testing"

	appsv1 "k8s.io/api/apps/v1"
	corev1 "k8s.io/api/core/v1"
	networkingv1 "k8s.io/api/networking/v1"
	utilyaml "k8s.io/apimachinery/pkg/util/yaml"
)

// Render the actual overlay so patch merging cannot accidentally remove base
// Redis, ConfigMap or ticket mounts while adding the managed TLS wiring.
func TestSandboxTerminalOverlayRendersScopedTLSAndNetwork(t *testing.T) {
	kubectl, err := exec.LookPath("kubectl")
	if err != nil {
		t.Skip("kubectl not installed; overlay rendering not verified")
	}
	overlay := filepath.Join(manifestBase(t), "..", "overlays", "sandbox-terminal")
	output, err := exec.Command(kubectl, "kustomize", overlay).CombinedOutput()
	if err != nil {
		t.Fatalf("render overlay: %v: %s", err, output)
	}
	decoder := utilyaml.NewYAMLToJSONDecoder(bytes.NewReader(output))
	var deployment appsv1.Deployment
	var policy networkingv1.NetworkPolicy
	for {
		var raw json.RawMessage
		if err := decoder.Decode(&raw); err == io.EOF {
			break
		} else if err != nil {
			t.Fatal(err)
		}
		if len(raw) == 0 {
			continue
		}
		var meta struct {
			Kind     string `json:"kind"`
			Metadata struct {
				Name string `json:"name"`
			} `json:"metadata"`
		}
		if err = json.Unmarshal(raw, &meta); err != nil {
			t.Fatal(err)
		}
		switch {
		case meta.Kind == "Deployment" && meta.Metadata.Name == "ani-session-gateway":
			if err = json.Unmarshal(raw, &deployment); err != nil {
				t.Fatal(err)
			}
		case meta.Kind == "NetworkPolicy" && meta.Metadata.Name == "ani-session-gateway-managed-terminal":
			if err = json.Unmarshal(raw, &policy); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(deployment.Spec.Template.Spec.Containers) != 1 {
		t.Fatal("missing application container")
	}
	pod := deployment.Spec.Template.Spec
	container := pod.Containers[0]
	baseCfg := false
	for _, entry := range container.EnvFrom {
		if entry.ConfigMapRef != nil && entry.ConfigMapRef.Name == "ani-session-gateway" {
			baseCfg = true
		}
	}
	if !baseCfg {
		t.Fatal("base runtime ConfigMap was lost")
	}
	variables := map[string]corev1.EnvVar{}
	for _, v := range container.Env {
		variables[v.Name] = v
	}
	for name, key := range map[string]string{"RESOURCE_INSTANCE_ADDR": "resource-address", "RESOURCE_INSTANCE_SERVER_NAME": "resource-server-name", "RESOURCE_INSTANCE_CLUSTER_ID": "resource-cluster-id"} {
		v, ok := variables[name]
		if !ok || v.ValueFrom == nil || v.ValueFrom.ConfigMapKeyRef == nil || v.ValueFrom.ConfigMapKeyRef.Name != "ani-session-gateway-managed-targets" || v.ValueFrom.ConfigMapKeyRef.Key != key || v.ValueFrom.ConfigMapKeyRef.Optional != nil && *v.ValueFrom.ConfigMapKeyRef.Optional {
			t.Fatalf("missing required %s", name)
		}
	}
	for _, name := range []string{"REDIS_URL", "TICKET_ENCRYPTION_KEY_FILE", "INSTANCE_CONTROL_CA_FILE", "INSTANCE_CONTROL_CERT_FILE", "INSTANCE_CONTROL_KEY_FILE", "RESOURCE_INSTANCE_CA_FILE", "RESOURCE_INSTANCE_CERT_FILE", "RESOURCE_INSTANCE_KEY_FILE"} {
		if _, ok := variables[name]; !ok {
			t.Fatalf("missing env %s", name)
		}
	}
	mounts := map[string]corev1.VolumeMount{}
	for _, m := range container.VolumeMounts {
		mounts[m.Name] = m
	}
	volumes := map[string]corev1.Volume{}
	for _, v := range pod.Volumes {
		volumes[v.Name] = v
	}
	for name, secret := range map[string]string{"ticket-key": "ani-session-gateway-secrets", "resource-client-tls": "ani-session-gateway-resource-client-tls", "control-server-tls": "ani-session-gateway-control-server-tls"} {
		m, mok := mounts[name]
		v, vok := volumes[name]
		if !mok || !m.ReadOnly || !vok || v.Secret == nil || v.Secret.SecretName != secret {
			t.Fatalf("missing read-only volume %s", name)
		}
	}
	if policy.Spec.PodSelector.MatchLabels["app.kubernetes.io/name"] != "ani-session-gateway" || len(policy.Spec.Ingress) != 1 || len(policy.Spec.Egress) != 1 {
		t.Fatal("unexpected managed terminal policy scope")
	}
	in := policy.Spec.Ingress[0]
	out := policy.Spec.Egress[0]
	if len(in.From) != 1 || len(in.Ports) != 1 || len(out.To) != 1 || len(out.Ports) != 1 {
		t.Fatal("policy adds unrestricted peers or ports")
	}
	ip, ep := in.From[0], out.To[0]
	if ip.NamespaceSelector == nil || ip.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "ani-system" || ip.PodSelector == nil || len(ip.PodSelector.MatchLabels) != 1 || ip.PodSelector.MatchLabels["app"] != "ani-governance" || in.Ports[0].Port == nil || in.Ports[0].Port.IntVal != 9090 {
		t.Fatal("unexpected Governance ingress")
	}
	if ep.NamespaceSelector == nil || ep.NamespaceSelector.MatchLabels["kubernetes.io/metadata.name"] != "ani-system" || ep.PodSelector == nil || len(ep.PodSelector.MatchLabels) != 1 || ep.PodSelector.MatchLabels["app"] != "ani-resource-service" || out.Ports[0].Port == nil || out.Ports[0].Port.IntVal != 9446 {
		t.Fatal("unexpected Resource egress")
	}
}
