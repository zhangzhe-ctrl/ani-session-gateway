//go:build instancelive

package kubernetesruntime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
	sessionpb "github.com/zhangzhe-ctrl/ani-session-gateway/api/gen/session/v1"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/apis/meta/v1/unstructured"
	"k8s.io/apimachinery/pkg/runtime/schema"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/client-go/dynamic"
	"k8s.io/client-go/tools/clientcmd"
)

// This is an explicitly separate CPU infrastructure probe. It uses a real
// Notebook controller, CSI volume and the production SG daemon/Redis/exec.
// It never accepts a GPU CREATE, changes quota or manufactures a GPU plan.
func TestNotebookCPUInfrastructureLive(t *testing.T) {
	path := os.Getenv("INSTANCE_CPU_LIVE_CONFIG")
	if path == "" {
		t.Skip("requires explicit owned experiment")
	}
	var c struct{ TenantID, InstanceID, Namespace, NamespaceUID, Notebook, PVC, Task, ClusterUID, StorageClass, Image, Directory, DriverKubeconfig, ControlAddress string }
	body, e := os.ReadFile(path)
	if e != nil {
		t.Fatal("private experiment unavailable")
	}
	if e = json.Unmarshal(body, &c); e != nil {
		t.Fatal(e)
	}
	if c.Namespace != "ani-tenant-"+c.TenantID || c.Task != "instance-mvp-v15" || !strings.Contains(c.Image, "@sha256:") {
		t.Fatal("invalid experiment scope")
	}
	cfg, e := clientcmd.BuildConfigFromFlags("", c.DriverKubeconfig)
	if e != nil {
		t.Fatal(e)
	}
	if cfg.Insecure || cfg.Host != "https://172.16.101.10:6443" {
		t.Fatal("unexpected API")
	}
	client, e := dynamic.NewForConfig(cfg)
	if e != nil {
		t.Fatal(e)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 7*time.Minute)
	defer cancel()
	nsGVR := schema.GroupVersionResource{Version: "v1", Resource: "namespaces"}
	ns, e := client.Resource(nsGVR).Get(ctx, c.Namespace, metav1.GetOptions{})
	if e != nil || string(ns.GetUID()) != c.NamespaceUID || ns.GetLabels()["instance.ani.io/task"] != c.Task {
		t.Fatal("namespace identity mismatch")
	}
	cluster, e := client.Resource(nsGVR).Get(ctx, "kube-system", metav1.GetOptions{})
	if e != nil || string(cluster.GetUID()) != c.ClusterUID {
		t.Fatal("cluster identity mismatch")
	}
	sc, e := client.Resource(schema.GroupVersionResource{Group: "storage.k8s.io", Version: "v1", Resource: "storageclasses"}).Get(ctx, c.StorageClass, metav1.GetOptions{})
	if e != nil {
		t.Fatal(e)
	}
	if v, _, _ := unstructured.NestedString(sc.Object, "reclaimPolicy"); v != "Delete" {
		t.Fatal("unexpected reclamation policy")
	}
	pvcGVR := schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumeclaims"}
	nbGVR := schema.GroupVersionResource{Group: "kubeflow.org", Version: "v1", Resource: "notebooks"}
	podGVR := schema.GroupVersionResource{Version: "v1", Resource: "pods"}
	pvGVR := schema.GroupVersionResource{Version: "v1", Resource: "persistentvolumes"}
	labels := map[string]any{"instance.ani.io/task": c.Task, "ani.kubercloud.io/tenant-id": c.TenantID, "ani.kubercloud.io/instance": c.Notebook}
	pvc, e := client.Resource(pvcGVR).Namespace(c.Namespace).Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "v1", "kind": "PersistentVolumeClaim", "metadata": map[string]any{"name": c.PVC, "namespace": c.Namespace, "labels": labels}, "spec": map[string]any{"storageClassName": c.StorageClass, "accessModes": []any{"ReadWriteOnce"}, "resources": map[string]any{"requests": map[string]any{"storage": "1Gi"}}}}}, metav1.CreateOptions{})
	if e != nil {
		t.Fatal(e)
	}
	pvcUID := pvc.GetUID()
	var nbUID types.UID
	deleteOwned := func(ctx context.Context, gvr schema.GroupVersionResource, name string, uid types.UID) error {
		policy := metav1.DeletePropagationForeground
		return client.Resource(gvr).Namespace(c.Namespace).Delete(ctx, name, metav1.DeleteOptions{PropagationPolicy: &policy, Preconditions: &metav1.Preconditions{UID: &uid}})
	}
	// A failure preserves diagnostics and only requests deletion of recorded UIDs.
	t.Cleanup(func() {
		cleanup, done := context.WithTimeout(context.Background(), 20*time.Second)
		defer done()
		if nbUID != "" {
			_ = deleteOwned(cleanup, nbGVR, c.Notebook, nbUID)
		}
		_ = deleteOwned(cleanup, pvcGVR, c.PVC, pvcUID)
	})
	podSpec := map[string]any{"serviceAccountName": "workload", "automountServiceAccountToken": false, "securityContext": map[string]any{"runAsUser": int64(1000), "runAsGroup": int64(1000), "fsGroup": int64(1000)}, "containers": []any{map[string]any{"name": "workspace", "image": c.Image, "imagePullPolicy": "IfNotPresent", "workingDir": "/workspace", "command": []any{"/bin/sh", "-c", "trap 'exit 0' TERM INT; while :; do sleep 60; done"}, "securityContext": map[string]any{"allowPrivilegeEscalation": false, "capabilities": map[string]any{"drop": []any{"ALL"}}}, "resources": map[string]any{"requests": map[string]any{"cpu": "100m", "memory": "64Mi"}, "limits": map[string]any{"cpu": "500m", "memory": "128Mi"}}, "env": []any{map[string]any{"name": "ANI_INSTANCE_POD_UID", "valueFrom": map[string]any{"fieldRef": map[string]any{"fieldPath": "metadata.uid"}}}}, "volumeMounts": []any{map[string]any{"name": "workspace", "mountPath": "/workspace"}}}}, "volumes": []any{map[string]any{"name": "workspace", "persistentVolumeClaim": map[string]any{"claimName": c.PVC}}}}
	nb, e := client.Resource(nbGVR).Namespace(c.Namespace).Create(ctx, &unstructured.Unstructured{Object: map[string]any{"apiVersion": "kubeflow.org/v1", "kind": "Notebook", "metadata": map[string]any{"name": c.Notebook, "namespace": c.Namespace, "labels": labels, "annotations": map[string]any{"instance.ani.io/intent-version": "1", "scheduling.volcano.sh/queue-name": "default", "volcano.sh/vgpu-mode": "hami-core", "instance.ani.io/projection-proof": c.InstanceID}}, "spec": map[string]any{"template": map[string]any{"spec": podSpec}}}}, metav1.CreateOptions{})
	if e != nil {
		t.Fatal(e)
	}
	nbUID = nb.GetUID()
	poll := func(name string, fn func() bool) {
		t.Helper()
		end := time.Now().Add(2 * time.Minute)
		for time.Now().Before(end) {
			if fn() {
				return
			}
			select {
			case <-ctx.Done():
				t.Fatal(name, "cancelled")
			case <-time.After(time.Second):
			}
		}
		t.Fatal(name, "did not converge")
	}
	podUID := ""
	ready := func() bool {
		list, e := client.Resource(podGVR).Namespace(c.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "ani.kubercloud.io/instance=" + c.Notebook})
		if e != nil || len(list.Items) != 1 {
			return false
		}
		p := list.Items[0]
		phase, _, _ := unstructured.NestedString(p.Object, "status", "phase")
		conditions, _, _ := unstructured.NestedSlice(p.Object, "status", "conditions")
		for _, v := range conditions {
			x, _ := v.(map[string]any)
			if phase == "Running" && x["type"] == "Ready" && x["status"] == "True" && p.GetDeletionTimestamp() == nil {
				podUID = string(p.GetUID())
				return true
			}
		}
		return false
	}
	poll("CPU Notebook Ready", ready)
	set, e := client.Resource(schema.GroupVersionResource{Group: "apps", Version: "v1", Resource: "statefulsets"}).Namespace(c.Namespace).Get(ctx, c.Notebook, metav1.GetOptions{})
	if e != nil {
		t.Fatal(e)
	}
	owned := func(object *unstructured.Unstructured, kind string, uid types.UID) bool {
		for _, ref := range object.GetOwnerReferences() {
			if ref.Kind == kind && ref.UID == uid && ref.Controller != nil && *ref.Controller {
				return true
			}
		}
		return false
	}
	if !owned(set, "Notebook", nbUID) {
		t.Fatal("StatefulSet parent UID mismatch")
	}
	projected, e := client.Resource(podGVR).Namespace(c.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "ani.kubercloud.io/instance=" + c.Notebook})
	if e != nil || len(projected.Items) != 1 {
		t.Fatal("projection absent")
	}
	actualPod := projected.Items[0]
	if !owned(&actualPod, "StatefulSet", set.GetUID()) {
		t.Fatal("Pod parent UID mismatch")
	}
	for key, value := range map[string]string{"scheduling.volcano.sh/queue-name": "default", "volcano.sh/vgpu-mode": "hami-core", "instance.ani.io/projection-proof": c.InstanceID} {
		if actualPod.GetAnnotations()[key] != value {
			t.Fatal("Notebook top-level annotation lost in Pod projection", key)
		}
	}
	pvc, e = client.Resource(pvcGVR).Namespace(c.Namespace).Get(ctx, c.PVC, metav1.GetOptions{})
	if e != nil || pvc.GetUID() != pvcUID {
		t.Fatal("workspace UID changed")
	}
	volume, _, _ := unstructured.NestedString(pvc.Object, "spec", "volumeName")
	if volume == "" {
		t.Fatal("workspace binding absent")
	}
	pv, e := client.Resource(pvGVR).Get(ctx, volume, metav1.GetOptions{})
	if e != nil {
		t.Fatal(e)
	}
	pvUID := pv.GetUID()
	claim, _, _ := unstructured.NestedString(pv.Object, "spec", "claimRef", "uid")
	if claim != string(pvcUID) {
		t.Fatal("PV claim UID mismatch")
	}
	projectionOnly := os.Getenv("INSTANCE_CPU_PROJECTION_ONLY") == "1"
	if !projectionOnly {
		ca, e := os.ReadFile(filepath.Join(c.Directory, "ca.pem"))
		if e != nil {
			t.Fatal(e)
		}
		roots := x509.NewCertPool()
		if !roots.AppendCertsFromPEM(ca) {
			t.Fatal("invalid private CA")
		}
		cert, e := tls.LoadX509KeyPair(filepath.Join(c.Directory, "gov-control.pem"), filepath.Join(c.Directory, "gov-control.key"))
		if e != nil {
			t.Fatal(e)
		}
		connection, e := grpc.NewClient(c.ControlAddress, grpc.WithTransportCredentials(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: "ani-session-gateway"})))
		if e != nil {
			t.Fatal(e)
		}
		defer connection.Close()
		sessions := sessionpb.NewSessionServiceClient(connection)
		count := 0
		runID := strconv.FormatInt(time.Now().UnixNano(), 10)
		shell := func(command, want string) {
			t.Helper()
			count++
			deadline, done := context.WithTimeout(ctx, 25*time.Second)
			defer done()
			reply, e := sessions.CreateSession(deadline, &sessionpb.CreateSessionRequest{RequestId: "cpu-probe-" + runID + "-" + strconv.Itoa(count), IdempotencyKey: "cpu-probe-" + runID + "-" + strconv.Itoa(count), Principal: &sessionpb.Principal{TenantId: c.TenantID, SubjectId: "experiment-admin"}, Target: &sessionpb.Target{InstanceId: c.InstanceID, WorkloadName: c.Notebook, WorkloadKind: sessionpb.WorkloadKind_WORKLOAD_KIND_CONTAINER}, Mode: &sessionpb.CreateSessionRequest_Exec{Exec: &sessionpb.ExecOptions{Container: "workspace", Command: []string{"/bin/sh"}, Rows: 24, Cols: 80}}})
			if e != nil {
				t.Fatal("trusted control session failed", e)
			}
			ws, response, e := websocket.Dial(deadline, reply.ConnectUrl, &websocket.DialOptions{Subprotocols: []string{"ani.terminal.v1"}, HTTPHeader: http.Header{"Origin": []string{"http://instance-lab"}}})
			if response != nil && e != nil {
				_ = response.Body.Close()
			}
			if e != nil {
				t.Fatal("WebSocket admission failed")
			}
			defer ws.CloseNow()
			raw, _ := json.Marshal(map[string]string{"type": "stdin", "data": command + "\nexit\n"})
			if e = ws.Write(deadline, websocket.MessageText, raw); e != nil {
				t.Fatal("stdin failed")
			}
			output := ""
			for {
				_, raw, e = ws.Read(deadline)
				if e != nil {
					t.Fatal("terminal stream failed")
				}
				var frame struct {
					Type, Data string
					Code       json.RawMessage
				}
				if e = json.Unmarshal(raw, &frame); e != nil {
					t.Fatal("invalid terminal frame")
				}
				if frame.Type == "stdout" {
					output += frame.Data
				}
				if frame.Type == "error" {
					t.Fatal("remote exec failed")
				}
				if frame.Type == "exit" {
					if string(frame.Code) != "0" || !strings.Contains(output, want) {
						t.Fatal("command or persisted-file assertion failed")
					}
					return
				}
			}
		}
		shell("test $(id -u) -eq 1000 && test $(id -g) -eq 1000 && printf cpu-retained > /workspace/instance-proof.txt && cat /workspace/instance-proof.txt", "cpu-retained")
		version := int64(1)
		for round := 0; round < 2; round++ {
			originalPod := podUID
			for _, stop := range []bool{true, false} {
				nb, e = client.Resource(nbGVR).Namespace(c.Namespace).Get(ctx, c.Notebook, metav1.GetOptions{})
				if e != nil || nb.GetUID() != nbUID {
					t.Fatal("Notebook UID changed")
				}
				version++
				ops := []map[string]any{{"op": "test", "path": "/metadata/uid", "value": string(nbUID)}, {"op": "test", "path": "/metadata/resourceVersion", "value": nb.GetResourceVersion()}, {"op": "add", "path": "/metadata/annotations/instance.ani.io~1intent-version", "value": strconv.FormatInt(version, 10)}}
				if stop {
					ops = append(ops, map[string]any{"op": "add", "path": "/metadata/annotations/kubeflow-resource-stopped", "value": "true"})
				} else {
					ops = append(ops, map[string]any{"op": "remove", "path": "/metadata/annotations/kubeflow-resource-stopped"})
				}
				raw, _ := json.Marshal(ops)
				if _, e = client.Resource(nbGVR).Namespace(c.Namespace).Patch(ctx, c.Notebook, types.JSONPatchType, raw, metav1.PatchOptions{}); e != nil {
					t.Fatal("conditional native intent failed", e)
				}
				if stop {
					poll("native Stop removed Pod", func() bool {
						pods, e := client.Resource(podGVR).Namespace(c.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "ani.kubercloud.io/instance=" + c.Notebook})
						return e == nil && len(pods.Items) == 0
					})
				} else {
					poll("native Start recreated Ready Pod", ready)
					if podUID == originalPod {
						t.Fatal("Start did not replace Pod")
					}
				}
				pvc, e = client.Resource(pvcGVR).Namespace(c.Namespace).Get(ctx, c.PVC, metav1.GetOptions{})
				if e != nil || pvc.GetUID() != pvcUID {
					t.Fatal("Stop/Start replaced workspace")
				}
			}
			shell("cat /workspace/instance-proof.txt", "cpu-retained")
		}
	}
	if e = deleteOwned(ctx, nbGVR, c.Notebook, nbUID); e != nil {
		t.Fatal(e)
	}
	poll("Notebook compute cleanup", func() bool {
		_, e := client.Resource(nbGVR).Namespace(c.Namespace).Get(ctx, c.Notebook, metav1.GetOptions{})
		pods, p := client.Resource(podGVR).Namespace(c.Namespace).List(ctx, metav1.ListOptions{LabelSelector: "ani.kubercloud.io/instance=" + c.Notebook})
		return cpuStatusCode(e) == 404 && p == nil && len(pods.Items) == 0
	})
	if e = deleteOwned(ctx, pvcGVR, c.PVC, pvcUID); e != nil {
		t.Fatal(e)
	}
	poll("exclusive PVC and PV reclamation", func() bool {
		_, e := client.Resource(pvcGVR).Namespace(c.Namespace).Get(ctx, c.PVC, metav1.GetOptions{})
		current, p := client.Resource(pvGVR).Get(ctx, volume, metav1.GetOptions{})
		if p == nil && current.GetUID() != pvUID {
			t.Fatal("PV replaced unexpectedly")
		}
		return cpuStatusCode(e) == 404 && cpuStatusCode(p) == 404
	})
	// Save identifiers only; tickets, terminal contents and credentials stay private.
	cycles := 2
	resultName := "cpu-result.json"
	if projectionOnly {
		cycles = 0
		resultName = "cpu-projection-result.json"
	}
	receipt, _ := json.MarshalIndent(map[string]any{"kind": "cpu_infrastructure_only", "namespace": c.Namespace, "namespace_uid": c.NamespaceUID, "notebook_uid": nbUID, "pvc_uid": pvcUID, "pv_uid": pvUID, "pv_name": volume, "stop_start_cycles": cycles, "sg_actual_exec": !projectionOnly, "workspace_retained": !projectionOnly, "annotation_projection": true, "statefulset_owner_uid": set.GetUID(), "compute_and_volume_reclaimed": true, "gpu_product_flow": "not_verified"}, "", "  ")
	if e = os.WriteFile(filepath.Join(c.Directory, resultName), append(receipt, '\n'), 0600); e != nil {
		t.Fatal(e)
	}
	if projectionOnly {
		t.Log("actual Notebook top-level annotation projection and UID owner chain verified; owned Notebook/PVC/PV reclaimed")
		return
	}
	t.Log("actual CPU Notebook/CSI/SG exec: two native Stop/Start cycles preserved original PVC and file; final Notebook/PVC/PV absent; GPU product flow not_verified")
}

func cpuStatusCode(err error) int {
	if err == nil {
		return 0
	}
	type apiStatus interface{ Status() metav1.Status }
	if s, ok := err.(apiStatus); ok {
		return int(s.Status().Code)
	}
	return 0
}
