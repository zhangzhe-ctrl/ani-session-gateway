package memory

import (
	"context"
	"crypto/subtle"
	"sync"
	"time"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
)

type Store struct {
	mu       sync.Mutex
	sessions map[string]session.Session
	byKey    map[string]string
	leases   map[string]session.SessionLease
}

func New() *Store {
	return &Store{sessions: map[string]session.Session{}, byKey: map[string]string{}, leases: map[string]session.SessionLease{}}
}
func (*Store) Ready(context.Context) error { return nil }
func (*Store) Mode() string                { return "memory" }

func (s *Store) CreateOrGet(_ context.Context, key string, candidate session.Session) (session.Session, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(candidate.CreatedAt)
	if id, ok := s.byKey[key]; ok {
		existing := s.sessions[id]
		if subtle.ConstantTimeCompare(existing.RequestFingerprint[:], candidate.RequestFingerprint[:]) != 1 {
			return session.Session{}, false, session.ErrConflict
		}
		if existing.State != session.StateIssued || !candidate.CreatedAt.Before(existing.TicketExpiresAt) {
			if existing.State == session.StateIssued {
				existing.State = session.StateClosed
				existing.ClosedAt = candidate.CreatedAt
				existing.CloseReason = "ticket_expired"
				existing.TicketCiphertext = nil
				s.sessions[id] = existing
			}
			return session.Session{}, false, session.ErrFailedPrecondition
		}
		return clone(existing), true, nil
	}
	s.byKey[key] = candidate.ID
	s.sessions[candidate.ID] = clone(candidate)
	return clone(candidate), false, nil
}

func (s *Store) ClaimAndReserve(_ context.Context, id string, digest [32]byte, now time.Time, limits session.ClaimLimits) (session.SessionLease, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	stored, ok := s.sessions[id]
	if !ok {
		return session.SessionLease{}, session.ErrNotFound
	}
	if stored.State != session.StateIssued || !now.Before(stored.TicketExpiresAt) {
		if stored.State == session.StateIssued {
			stored.State = session.StateClosed
			stored.ClosedAt = now
			stored.CloseReason = "ticket_expired"
			stored.TicketCiphertext = nil
			s.sessions[id] = stored
		}
		return session.SessionLease{}, session.ErrFailedPrecondition
	}
	if subtle.ConstantTimeCompare(stored.TicketDigest[:], digest[:]) != 1 {
		return session.SessionLease{}, session.ErrInvalidTicket
	}
	if len(s.leases) >= limits.MaxActive {
		return session.SessionLease{}, session.ErrCapacity
	}
	activeSubject := 0
	for _, lease := range s.leases {
		if lease.Session.SubjectID == stored.SubjectID {
			activeSubject++
		}
	}
	if activeSubject >= limits.MaxActivePerSubject {
		return session.SessionLease{}, session.ErrCapacity
	}
	leaseID := id + ":lease"
	stored.State = session.StateClaimed
	stored.ClaimedAt = now
	stored.ExpiresAt = now.Add(limits.SessionMaxDuration)
	stored.TombstoneExpiresAt = stored.ExpiresAt.Add(limits.IdempotencyTTL)
	stored.LeaseID = leaseID
	stored.TicketCiphertext = nil
	lease := session.SessionLease{ID: leaseID, Session: clone(stored), ExpiresAt: stored.ExpiresAt}
	s.sessions[id] = stored
	s.leases[id] = lease
	return lease, nil
}

func (s *Store) CloseAndRelease(_ context.Context, id, leaseID, reason string, now time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.cleanup(now)
	stored, ok := s.sessions[id]
	if !ok {
		return session.ErrNotFound
	}
	if stored.State == session.StateClosed {
		return nil
	}
	if stored.State != session.StateClaimed || stored.LeaseID != leaseID {
		return session.ErrFailedPrecondition
	}
	delete(s.leases, id)
	stored.State = session.StateClosed
	stored.ClosedAt = now
	stored.CloseReason = reason
	stored.TicketCiphertext = nil
	s.sessions[id] = stored
	return nil
}

func (s *Store) cleanup(now time.Time) {
	for id, lease := range s.leases {
		if !now.Before(lease.ExpiresAt) {
			delete(s.leases, id)
			stored := s.sessions[id]
			stored.State = session.StateClosed
			stored.ClosedAt = lease.ExpiresAt
			stored.CloseReason = "max_duration"
			stored.TicketCiphertext = nil
			s.sessions[id] = stored
		}
	}
	for key, id := range s.byKey {
		if stored, ok := s.sessions[id]; ok && !now.Before(stored.TombstoneExpiresAt) {
			delete(s.byKey, key)
			delete(s.sessions, id)
			delete(s.leases, id)
		}
	}
}

func clone(value session.Session) session.Session {
	value.Command = append([]string(nil), value.Command...)
	value.TicketCiphertext = append([]byte(nil), value.TicketCiphertext...)
	return value
}
