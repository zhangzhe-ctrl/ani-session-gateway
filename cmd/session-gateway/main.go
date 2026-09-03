package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net"
	"net/http"
	"os"
	"os/signal"
	"syscall"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/config"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/observability"
	kubernetesruntime "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime/kubernetes"
	kubevirtruntime "github.com/zhangzhe-ctrl/ani-session-gateway/internal/runtime/kubevirt"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/store"
	grpctransport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/grpc"
	httptransport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/http"
	websockettransport "github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/websocket"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/transport/websocket/connectedsession"
	typedcorev1 "k8s.io/client-go/kubernetes/typed/core/v1"
	"k8s.io/client-go/rest"
)

func main() {
	if err := run(); err != nil {
		slog.Error("session gateway stopped", "error", err)
		os.Exit(1)
	}
}

func run() (runErr error) {
	cfg, err := config.Load()
	if err != nil {
		return fmt.Errorf("load config: %w", err)
	}
	tracing, err := observability.InitTracing(context.Background(), observability.TracingConfig{ServiceName: cfg.OTelServiceName, ExporterEndpoint: cfg.OTelExporterEndpoint})
	if err != nil {
		return fmt.Errorf("initialize tracing: %w", err)
	}
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGracePeriod)
		defer cancel()
		if err := tracing.Shutdown(shutdownCtx); err != nil && runErr == nil {
			runErr = fmt.Errorf("shutdown tracing: %w", err)
		}
	}()
	selection, err := store.Select(context.Background(), cfg)
	if err != nil {
		return fmt.Errorf("select session store: %w", err)
	}
	defer func() {
		if selection.Shutdown != nil {
			if err := selection.Shutdown(); err != nil && runErr == nil {
				runErr = fmt.Errorf("shutdown session store: %w", err)
			}
		}
	}()
	selectedStore := observability.TraceStore(selection.Store)
	probes := observability.NewHandler()
	probes.RegisterStore(selectedStore.Mode(), selection.Degraded)
	probes.SetDependencyCheck(selectedStore.Ready)
	if selection.Degraded {
		slog.Warn("store_degraded", "store_mode", selectedStore.Mode(), "local", true)
	}
	manager, err := session.NewManager(selectedStore, session.ManagerConfig{PublicWSBaseURL: cfg.PublicWSBaseURL, TicketKey: cfg.TicketKey, TicketTTL: cfg.TicketTTL, SessionMaxDuration: cfg.SessionMaxDuration, IdempotencyTTL: cfg.IdempotencyTTL, MaxActive: cfg.MaxActiveSessions, MaxActivePerSubject: cfg.MaxActivePerSubject, Observer: probes.SessionMetrics()})
	if err != nil {
		return fmt.Errorf("initialize session manager: %w", err)
	}
	kubernetesConfig, err := rest.InClusterConfig()
	if err != nil {
		return fmt.Errorf("load in-cluster Kubernetes config: %w", err)
	}
	kubernetesClient, err := typedcorev1.NewForConfig(kubernetesConfig)
	if err != nil {
		return fmt.Errorf("initialize Kubernetes client: %w", err)
	}
	execRuntime := kubernetesruntime.NewExecAdapter(kubernetesConfig, kubernetesClient)
	vmRuntime, err := kubevirtruntime.NewAdapter(kubernetesConfig, cfg.WSHandshakeTimeout)
	if err != nil {
		return fmt.Errorf("initialize KubeVirt client: %w", err)
	}
	connectedSessions, err := connectedsession.New(connectedsession.Dependencies{
		Manager: manager,
		Exec:    execRuntime,
		VM:      vmRuntime,
		Clock:   connectedsession.SystemClock{},
		Observer: observability.NewConnectedSessionObserver(
			probes.SessionMetrics(),
		),
	}, connectedsession.Policy{
		MaxMessageBytes: cfg.WSMaxMessageBytes,
		IdleTimeout:     cfg.WSIdleTimeout,
		WriteTimeout:    cfg.WSHandshakeTimeout,
		InboundQueue:    32,
		OutboundQueue:   32,
	})
	if err != nil {
		return fmt.Errorf("initialize Connected Session module: %w", err)
	}
	websocketHandler, err := websockettransport.NewHandler(manager, connectedSessions, websockettransport.Config{AllowedOrigins: cfg.AllowedOrigins, CleanupTimeout: cfg.WSHandshakeTimeout})
	if err != nil {
		return fmt.Errorf("initialize WebSocket admission: %w", err)
	}
	runCtx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	httpListener, err := net.Listen("tcp", cfg.HTTPAddr)
	if err != nil {
		return fmt.Errorf("listen HTTP: %w", err)
	}
	defer httpListener.Close()
	grpcListener, err := net.Listen("tcp", cfg.GRPCAddr)
	if err != nil {
		return fmt.Errorf("listen gRPC: %w", err)
	}
	defer grpcListener.Close()

	router := httptransport.NewRouter(probes)
	websockettransport.RegisterRoutes(router, websocketHandler)
	httpServer := &http.Server{Addr: cfg.HTTPAddr, Handler: router, ReadHeaderTimeout: cfg.WSHandshakeTimeout, BaseContext: func(net.Listener) context.Context { return runCtx }}
	grpcServer := grpctransport.New(grpctransport.NewSessionServer(manager))
	errorsCh := make(chan error, 2)
	go func() {
		if err := httpServer.Serve(httpListener); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errorsCh <- fmt.Errorf("serve HTTP: %w", err)
		}
	}()
	go func() {
		if err := grpcServer.Serve(grpcListener); err != nil {
			errorsCh <- fmt.Errorf("serve gRPC: %w", err)
		}
	}()
	probes.SetReady(true)
	slog.Info("session gateway started", "http_addr", cfg.HTTPAddr, "grpc_addr", cfg.GRPCAddr, "store_mode", selectedStore.Mode(), "local_degraded", selection.Degraded)

	select {
	case <-runCtx.Done():
	case err := <-errorsCh:
		return err
	}
	probes.SetReady(false)
	shutdownCtx, cancel := context.WithTimeout(context.Background(), cfg.ShutdownGracePeriod)
	defer cancel()
	grpcServer.GracefulStop()
	return httpServer.Shutdown(shutdownCtx)
}
