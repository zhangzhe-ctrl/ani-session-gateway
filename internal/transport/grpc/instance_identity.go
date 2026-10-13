package grpctransport

import (
	"context"
	"google.golang.org/grpc/credentials"
	"google.golang.org/grpc/peer"
)

func authenticatedGovernance(ctx context.Context) bool {
	p, ok := peer.FromContext(ctx)
	if !ok {
		return false
	}
	t, ok := p.AuthInfo.(credentials.TLSInfo)
	return ok && len(t.State.VerifiedChains) > 0 && len(t.State.PeerCertificates) > 0 && len(t.State.PeerCertificates[0].URIs) == 1 && t.State.PeerCertificates[0].URIs[0].String() == "spiffe://ani.internal/service/ani-governance"
}
