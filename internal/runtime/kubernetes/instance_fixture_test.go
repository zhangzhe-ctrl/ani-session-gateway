package kubernetesruntime

import (
	"bufio"
	"context"
	"crypto/tls"
	"crypto/x509"
	"encoding/json"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/go-chi/chi/v5"
	"github.com/redis/go-redis/v9"
	sessionpb "github.com/zhangzhe-ctrl/ani-session-gateway/api/gen/session/v1"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/observability"
	runtimeport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime"
	resourceruntime "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime/resource"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	redisstore "github.com/zhangzhe-ctrl/ani-session-gateway/internal/store/redis"
	grpctransport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/grpc"
	websockettransport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/websocket"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/websocket/connectedsession"
	"google.golang.org/grpc"
	"google.golang.org/grpc/credentials"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
	"k8s.io/client-go/tools/remotecommand"
)

type instanceFixtureVM struct{}

func (instanceFixtureVM) OpenSerial(context.Context, runtimeport.VMTarget) (runtimeport.ByteStream, error) {
	return nil, runtimeport.ErrUnavailable
}
func (instanceFixtureVM) OpenVNC(context.Context, runtimeport.VMTarget) (runtimeport.ByteStream, error) {
	return nil, runtimeport.ErrUnavailable
}

type instanceFixtureSessions struct {
	sessionpb.UnimplementedSessionServiceServer
	handler    *grpctransport.SessionServer
	mu         sync.Mutex
	pods       map[string]*corev1.Pod
	workspaces map[string]string
	root       string
}

func (s *instanceFixtureSessions) CreateSession(ctx context.Context, r *sessionpb.CreateSessionRequest) (*sessionpb.CreateSessionResponse, error) {
	reply, e := s.handler.CreateSession(ctx, r)
	if e != nil {
		return nil, e
	}
	b := r.Target.InstanceBinding
	s.mu.Lock()
	defer s.mu.Unlock()
	key := b.Namespace + "/" + b.PodName
	s.pods[key] = &corev1.Pod{ObjectMeta: metav1.ObjectMeta{Name: b.PodName, Namespace: b.Namespace, UID: types.UID(b.PodUid), Labels: map[string]string{"accelerator.ani.io/resource-tenant-id": r.Principal.TenantId, "accelerator.ani.io/resource-id": r.Target.InstanceId, "accelerator.ani.io/owner-service": "ani-resource-service"}}, Spec: corev1.PodSpec{Containers: []corev1.Container{{Name: b.ContainerName}}}, Status: corev1.PodStatus{Phase: corev1.PodRunning, Conditions: []corev1.PodCondition{{Type: corev1.PodReady, Status: corev1.ConditionTrue}}}}
	path := filepath.Join(s.root, r.Target.InstanceId)
	if e = os.MkdirAll(path, 0700); e != nil {
		return nil, e
	}
	s.workspaces[key] = path
	return reply, nil
}

// Real SG Manager/Redis/control mTLS/WebSocket/Connected Session/ExecAdapter and
// Resource validator. Only Kubernetes listing and remotecommand are external
// doubles. Shell files are task-local, not a claim about live PVC/GPU storage.
func TestInstanceSessionFixture(t *testing.T) {
	dir := os.Getenv("SG_INSTANCE_FIXTURE_DIR")
	if dir == "" {
		t.Skip("explicit private subprocess fixture")
	}
	var cfg struct{ CAFile, SGServerCertFile, SGServerKeyFile, ResourceAddress, SGClientCertFile, SGClientKeyFile, ClusterID, RedisAddress string }
	raw, e := os.ReadFile(os.Getenv("SG_INSTANCE_CONFIG_FILE"))
	if e != nil {
		t.Fatal(e)
	}
	if e = json.Unmarshal(raw, &cfg); e != nil {
		t.Fatal(e)
	}
	t.Setenv("RESOURCE_INSTANCE_ADDR", cfg.ResourceAddress)
	t.Setenv("RESOURCE_INSTANCE_SERVER_NAME", "ani-network-service")
	t.Setenv("RESOURCE_INSTANCE_CA_FILE", cfg.CAFile)
	t.Setenv("RESOURCE_INSTANCE_CERT_FILE", cfg.SGClientCertFile)
	t.Setenv("RESOURCE_INSTANCE_KEY_FILE", cfg.SGClientKeyFile)
	validator, closeValidator, e := resourceruntime.NewFromEnv()
	if e != nil {
		t.Fatal(e)
	}
	defer closeValidator()
	redisClient := redis.NewClient(&redis.Options{Addr: cfg.RedisAddress})
	defer redisClient.Close()
	if e = redisClient.Ping(context.Background()).Err(); e != nil {
		t.Fatal(e)
	}
	host := httptest.NewUnstartedServer(nil)
	defer host.Close()
	base, _ := url.Parse("ws://" + host.Listener.Addr().String() + "/api/v1/realtime")
	var ticketKey [32]byte
	copy(ticketKey[:], []byte("fixture-private-session-key-32byt"))
	manager, e := session.NewManager(redisstore.New(redisClient), session.ManagerConfig{InstanceValidator: validator, PublicWSBaseURL: base, TicketKey: ticketKey, TicketTTL: time.Minute, SessionMaxDuration: 2 * time.Minute, IdempotencyTTL: 2 * time.Minute, MaxActive: 20, MaxActivePerSubject: 10})
	if e != nil {
		t.Fatal(e)
	}
	sessions := &instanceFixtureSessions{handler: grpctransport.NewSessionServer(manager), pods: map[string]*corev1.Pod{}, workspaces: map[string]string{}, root: t.TempDir()}
	kube := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		sessions.mu.Lock()
		defer sessions.mu.Unlock()
		items := []corev1.Pod{}
		for _, p := range sessions.pods {
			items = append(items, *p)
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(&corev1.PodList{TypeMeta: metav1.TypeMeta{APIVersion: "v1", Kind: "PodList"}, Items: items})
	}))
	defer kube.Close()
	config := &rest.Config{Host: kube.URL}
	client, e := typedcorev1.NewForConfig(config)
	if e != nil {
		t.Fatal(e)
	}
	adapter := NewExecAdapter(config, client)
	adapter.ConfigureInstanceValidator(validator, cfg.ClusterID)
	adapter.newExecutor = func(_ *rest.Config, _ string, u *url.URL) (remotecommand.Executor, error) {
		command := append([]string(nil), u.Query()["command"]...)
		segments := splitFixtureExecPath(u.Path)
		sessions.mu.Lock()
		p := sessions.pods[segments]
		workspace := sessions.workspaces[segments]
		sessions.mu.Unlock()
		if p == nil || workspace == "" {
			return nil, runtimeport.ErrTargetNotFound
		}
		uid := string(p.UID)
		return executorFunc(func(ctx context.Context, streams remotecommand.StreamOptions) error {
			cmd := exec.CommandContext(ctx, command[0], command[1:]...)
			cmd.Dir = workspace
			cmd.Env = append(os.Environ(), "ANI_INSTANCE_POD_UID="+uid)
			input, e := cmd.StdinPipe()
			if e != nil {
				return e
			}
			defer input.Close()
			cmd.Stdout = streams.Stdout
			cmd.Stderr = streams.Stderr
			if e = cmd.Start(); e != nil {
				return e
			}
			go func() { _, _ = io.Copy(input, streams.Stdin); _ = input.Close() }()
			e = cmd.Wait()
			if exit, ok := e.(*exec.ExitError); ok {
				return exitError{code: exit.ExitCode()}
			}
			return e
		}), nil
	}
	probes := observability.NewHandler()
	connected, e := connectedsession.New(connectedsession.Dependencies{Manager: manager, Exec: adapter, VM: instanceFixtureVM{}, Clock: connectedsession.SystemClock{}, Observer: observability.NewConnectedSessionObserver(probes.SessionMetrics())}, connectedsession.Policy{MaxMessageBytes: 65536, IdleTimeout: time.Minute, WriteTimeout: 3 * time.Second, InboundQueue: 16, OutboundQueue: 16})
	if e != nil {
		t.Fatal(e)
	}
	ws, e := websockettransport.NewHandler(manager, connected, websockettransport.Config{AllowedOrigins: map[string]struct{}{"http://instance-fixture": {}}, CleanupTimeout: 3 * time.Second})
	if e != nil {
		t.Fatal(e)
	}
	router := chi.NewRouter()
	websockettransport.RegisterRoutes(router, ws)
	host.Config.Handler = router
	host.Start()
	ca, e := os.ReadFile(cfg.CAFile)
	if e != nil {
		t.Fatal(e)
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		t.Fatal("fixture CA invalid")
	}
	cert, e := tls.LoadX509KeyPair(cfg.SGServerCertFile, cfg.SGServerKeyFile)
	if e != nil {
		t.Fatal(e)
	}
	control := grpctransport.New(sessions, grpc.Creds(credentials.NewTLS(&tls.Config{MinVersion: tls.VersionTLS13, Certificates: []tls.Certificate{cert}, ClientCAs: roots, ClientAuth: tls.RequireAndVerifyClientCert})))
	listener, e := net.Listen("tcp", "127.0.0.1:0")
	if e != nil {
		t.Fatal(e)
	}
	done := make(chan error, 1)
	go func() { done <- control.Serve(listener) }()
	defer func() {
		control.Stop()
		if e := <-done; e != nil {
			t.Error(e)
		}
	}()
	raw, e = json.Marshal(map[string]string{"Address": listener.Addr().String()})
	if e != nil {
		t.Fatal(e)
	}
	if e = os.WriteFile(filepath.Join(dir, "ready.json"), raw, 0600); e != nil {
		t.Fatal(e)
	}
	scanner := bufio.NewScanner(os.Stdin)
	if !scanner.Scan() || scanner.Text() != "stop" {
		t.Fatal("fixture shutdown command required")
	}
}
func splitFixtureExecPath(path string) string {
	var namespace, name string
	segments := strings.Split(path, "/")
	for i, p := range segments {
		if p == "namespaces" && i+1 < len(segments) {
			namespace = segments[i+1]
		}
		if p == "pods" && i+1 < len(segments) {
			name = segments[i+1]
		}
	}
	return namespace + "/" + name
}
