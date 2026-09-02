package grpctransport

import (
	"context"
	"errors"

	sessionv1 "github.com/zhangzhe-ctrl/ani-session-gateway/api/gen/session/v1"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/observability"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	grpc_health "google.golang.org/grpc/health"
	healthv1 "google.golang.org/grpc/health/grpc_health_v1"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/timestamppb"
)

type SessionServer struct {
	sessionv1.UnimplementedSessionServiceServer
	manager *session.Manager
}

func NewSessionServer(manager *session.Manager) *SessionServer {
	return &SessionServer{manager: manager}
}

func (s *SessionServer) CreateSession(ctx context.Context, request *sessionv1.CreateSessionRequest) (*sessionv1.CreateSessionResponse, error) {
	if s.manager == nil {
		return nil, status.Error(codes.Unimplemented, "session creation is not configured")
	}
	mapped, err := mapRequest(request)
	if err != nil {
		return nil, status.Error(codes.InvalidArgument, "invalid session request")
	}
	issued, err := s.manager.Issue(ctx, mapped)
	if err != nil {
		return nil, mapError(err)
	}
	return &sessionv1.CreateSessionResponse{SessionId: issued.Session.ID, ConnectUrl: s.manager.ConnectURL(issued), ExpiresAt: timestamppb.New(issued.Session.TicketExpiresAt), Replayed: issued.Replayed}, nil
}

func New(sessionServer sessionv1.SessionServiceServer) *grpc.Server {
	server := grpc.NewServer(grpc.ChainUnaryInterceptor(func(ctx context.Context, request any, info *grpc.UnaryServerInfo, handler grpc.UnaryHandler) (any, error) {
		ctx, span := observability.StartSpan(ctx, info.FullMethod)
		defer span.End()
		return handler(ctx, request)
	}))
	if sessionServer == nil {
		sessionServer = &SessionServer{}
	}
	sessionv1.RegisterSessionServiceServer(server, sessionServer)
	health := grpc_health.NewServer()
	health.SetServingStatus("", healthv1.HealthCheckResponse_SERVING)
	health.SetServingStatus(sessionv1.SessionService_ServiceDesc.ServiceName, healthv1.HealthCheckResponse_SERVING)
	healthv1.RegisterHealthServer(server, health)
	return server
}

func mapRequest(request *sessionv1.CreateSessionRequest) (session.Request, error) {
	if request == nil || request.Principal == nil || request.Target == nil {
		return session.Request{}, errors.New("principal and target are required")
	}
	mapped := session.Request{RequestID: request.RequestId, IdempotencyKey: request.IdempotencyKey, TenantID: request.Principal.TenantId, SubjectID: request.Principal.SubjectId, InstanceID: request.Target.InstanceId, WorkloadName: request.Target.WorkloadName}
	switch request.Target.WorkloadKind {
	case sessionv1.WorkloadKind_WORKLOAD_KIND_CONTAINER:
		mapped.WorkloadKind = session.WorkloadContainer
	case sessionv1.WorkloadKind_WORKLOAD_KIND_GPU_CONTAINER:
		mapped.WorkloadKind = session.WorkloadGPUContainer
	case sessionv1.WorkloadKind_WORKLOAD_KIND_SANDBOX:
		mapped.WorkloadKind = session.WorkloadSandbox
	case sessionv1.WorkloadKind_WORKLOAD_KIND_VM:
		mapped.WorkloadKind = session.WorkloadVM
	default:
		return session.Request{}, errors.New("unsupported workload kind")
	}
	switch mode := request.Mode.(type) {
	case *sessionv1.CreateSessionRequest_Exec:
		if mode.Exec == nil {
			return session.Request{}, errors.New("exec options required")
		}
		if mode.Exec.Rows < 1 || mode.Exec.Rows > 4096 || mode.Exec.Cols < 1 || mode.Exec.Cols > 4096 {
			return session.Request{}, errors.New("invalid terminal size")
		}
		mapped.Mode, mapped.Container, mapped.Command, mapped.TTY = session.ModeExec, mode.Exec.Container, append([]string(nil), mode.Exec.Command...), mode.Exec.Tty
		mapped.Rows, mapped.Cols = uint16(mode.Exec.Rows), uint16(mode.Exec.Cols)
	case *sessionv1.CreateSessionRequest_VmConsole:
		if mode.VmConsole == nil {
			return session.Request{}, errors.New("VM console options required")
		}
		mapped.RequestedProtocol = mode.VmConsole.RequestedProtocol
		switch mode.VmConsole.Protocol {
		case sessionv1.VMConsoleOptions_PROTOCOL_SERIAL:
			mapped.Mode = session.ModeSerial
		case sessionv1.VMConsoleOptions_PROTOCOL_VNC:
			mapped.Mode = session.ModeVNC
		default:
			return session.Request{}, errors.New("unsupported VM console protocol")
		}
	default:
		return session.Request{}, errors.New("mode required")
	}
	return mapped, nil
}

func mapError(err error) error {
	switch {
	case errors.Is(err, session.ErrInvalidRequest):
		return status.Error(codes.InvalidArgument, "invalid session request")
	case errors.Is(err, session.ErrConflict):
		return status.Error(codes.AlreadyExists, "idempotency key conflicts with request")
	case errors.Is(err, session.ErrFailedPrecondition):
		return status.Error(codes.FailedPrecondition, "session is no longer claimable")
	case errors.Is(err, session.ErrCapacity):
		return status.Error(codes.ResourceExhausted, "session capacity exhausted")
	case errors.Is(err, session.ErrUnavailable):
		return status.Error(codes.Unavailable, "session store unavailable")
	default:
		return status.Error(codes.Internal, "session creation failed")
	}
}
