package resourceruntime

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"os"
	"strings"
	"time"

	pb "github.com/zhangzhe-ctrl/ani-resource-service/api/instance/terminal/v1"
	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
)

type Validator struct {
	client pb.InstanceTerminalServiceClient
}

func NewFromEnv() (*Validator, func(), error) {
	addr := os.Getenv("RESOURCE_INSTANCE_ADDR")
	if addr == "" {
		return nil, func() {}, nil
	}
	name := os.Getenv("RESOURCE_INSTANCE_SERVER_NAME")
	if name == "" || strings.ContainsAny(name, "*:/") {
		return nil, nil, fmt.Errorf("exact Resource instance server name required")
	}
	ca, e := os.ReadFile(os.Getenv("RESOURCE_INSTANCE_CA_FILE"))
	if e != nil {
		return nil, nil, fmt.Errorf("Resource instance CA unavailable")
	}
	roots := x509.NewCertPool()
	if !roots.AppendCertsFromPEM(ca) {
		return nil, nil, fmt.Errorf("Resource instance CA invalid")
	}
	cert, e := tls.LoadX509KeyPair(os.Getenv("RESOURCE_INSTANCE_CERT_FILE"), os.Getenv("RESOURCE_INSTANCE_KEY_FILE"))
	if e != nil {
		return nil, nil, fmt.Errorf("SG Resource identity unavailable")
	}
	cfg := &tls.Config{MinVersion: tls.VersionTLS13, RootCAs: roots, Certificates: []tls.Certificate{cert}, ServerName: name, VerifyConnection: func(cs tls.ConnectionState) error {
		if len(cs.VerifiedChains) == 0 || len(cs.PeerCertificates) == 0 {
			return fmt.Errorf("Resource peer unverified")
		}
		for _, v := range cs.PeerCertificates[0].DNSNames {
			if v == name && !strings.Contains(v, "*") {
				return nil
			}
		}
		return fmt.Errorf("Resource peer exact DNS mismatch")
	}}
	conn, e := grpc.NewClient(addr, grpc.WithDisableServiceConfig(), grpc.WithDisableRetry(), grpc.WithTransportCredentials(credentials.NewTLS(cfg)))
	if e != nil {
		return nil, nil, e
	}
	return &Validator{pb.NewInstanceTerminalServiceClient(conn)}, func() { _ = conn.Close() }, nil
}
func (v *Validator) Validate(ctx context.Context, tenant, id string, b session.InstanceBinding) error {
	if v == nil || v.client == nil || !b.Enabled {
		return session.ErrUnavailable
	}
	ctx, cancel := context.WithTimeout(metadata.NewOutgoingContext(ctx, metadata.MD{}), 3*time.Second)
	defer cancel()
	result, e := v.client.ValidateTerminalTarget(ctx, &pb.TerminalTarget{TenantId: tenant, InstanceId: id, Version: b.Version, ClusterId: b.ClusterID, Namespace: b.Namespace, NotebookName: b.NotebookName, NotebookUid: b.NotebookUID, PodName: b.PodName, PodUid: b.PodUID, ContainerName: b.ContainerName, Shell: b.Shell})
	if e != nil {
		if status.Code(e) == codes.FailedPrecondition || status.Code(e) == codes.NotFound {
			return session.ErrFailedPrecondition
		}
		return session.ErrUnavailable
	}
	if result == nil || !result.Valid {
		return session.ErrFailedPrecondition
	}
	return nil
}
