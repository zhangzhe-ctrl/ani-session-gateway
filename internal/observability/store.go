package observability

import (
	"context"
	"time"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
)

type tracedStore struct{ next session.Store }

func TraceStore(next session.Store) session.Store { return &tracedStore{next: next} }
func (s *tracedStore) Mode() string               { return s.next.Mode() }
func (s *tracedStore) Ready(ctx context.Context) error {
	ctx, span := StartSpan(ctx, "session_store.ready")
	defer span.End()
	return s.next.Ready(ctx)
}
func (s *tracedStore) CreateOrGet(ctx context.Context, key string, candidate session.Session) (session.Session, bool, error) {
	ctx, span := StartSpan(ctx, "session_store.create_or_get")
	defer span.End()
	return s.next.CreateOrGet(ctx, key, candidate)
}
func (s *tracedStore) ClaimAndReserve(ctx context.Context, id string, digest [32]byte, now time.Time, limits session.ClaimLimits) (session.SessionLease, error) {
	ctx, span := StartSpan(ctx, "session_store.claim_and_reserve")
	defer span.End()
	return s.next.ClaimAndReserve(ctx, id, digest, now, limits)
}
func (s *tracedStore) CloseAndRelease(ctx context.Context, id, leaseID, reason string, now time.Time) error {
	ctx, span := StartSpan(ctx, "session_store.close_and_release")
	defer span.End()
	return s.next.CloseAndRelease(ctx, id, leaseID, reason, now)
}
