package storetest

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"sync"
	"testing"
	"time"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
)

type Factory func(*testing.T) session.Store

func Run(t *testing.T, factory Factory) {
	t.Helper()
	t.Run("idempotency and state", func(t *testing.T) {
		store := factory(t)
		now := time.Now().UTC()
		first := candidate("one", "key", "subject", now)
		created, replayed, err := store.CreateOrGet(context.Background(), "key", first)
		if err != nil || replayed || created.ID != first.ID {
			t.Fatalf("create: replayed=%v err=%v", replayed, err)
		}
		replayCandidate := candidate("two", "key", "subject", now.Add(time.Second))
		replayCandidate.RequestFingerprint = first.RequestFingerprint
		replayedSession, replayed, err := store.CreateOrGet(context.Background(), "key", replayCandidate)
		if err != nil || !replayed || replayedSession.ID != first.ID {
			t.Fatalf("replay: session=%s replayed=%v err=%v", replayedSession.ID, replayed, err)
		}
		conflict := candidate("three", "key", "subject", now.Add(time.Second))
		conflict.RequestFingerprint = sha256.Sum256([]byte("different"))
		if _, _, err := store.CreateOrGet(context.Background(), "key", conflict); !errors.Is(err, session.ErrConflict) {
			t.Fatalf("conflict error=%v", err)
		}
		if _, err := store.ClaimAndReserve(context.Background(), first.ID, sha256.Sum256([]byte("wrong")), now, limits(10, 5, time.Minute)); !errors.Is(err, session.ErrInvalidTicket) {
			t.Fatalf("wrong ticket error=%v", err)
		}
		lease, err := store.ClaimAndReserve(context.Background(), first.ID, first.TicketDigest, now, limits(10, 5, time.Minute))
		if err != nil {
			t.Fatal(err)
		}
		if lease.Session.State != session.StateClaimed || len(lease.Session.TicketCiphertext) != 0 {
			t.Fatalf("claimed session leaked ciphertext: %#v", lease.Session)
		}
		wantTombstone := lease.ExpiresAt.Add(15 * time.Minute).Truncate(time.Millisecond)
		if !lease.Session.TombstoneExpiresAt.Truncate(time.Millisecond).Equal(wantTombstone) {
			t.Fatalf("claim tombstone=%s want %s", lease.Session.TombstoneExpiresAt, wantTombstone)
		}
		if _, _, err := store.CreateOrGet(context.Background(), "key", replayCandidate); !errors.Is(err, session.ErrFailedPrecondition) {
			t.Fatalf("claim replay error=%v", err)
		}
		if err := store.CloseAndRelease(context.Background(), first.ID, lease.ID, "normal", now); err != nil {
			t.Fatal(err)
		}
		if err := store.CloseAndRelease(context.Background(), first.ID, lease.ID, "duplicate", now); err != nil {
			t.Fatalf("duplicate close: %v", err)
		}
	})

	t.Run("100 concurrent claims", func(t *testing.T) {
		store := factory(t)
		now := time.Now().UTC()
		value := candidate("concurrent", "concurrent-key", "subject", now)
		if _, _, err := store.CreateOrGet(context.Background(), value.IdempotencyKey, value); err != nil {
			t.Fatal(err)
		}
		var wg sync.WaitGroup
		successes := 0
		var mu sync.Mutex
		for range 100 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				if _, err := store.ClaimAndReserve(context.Background(), value.ID, value.TicketDigest, now, limits(100, 100, time.Minute)); err == nil {
					mu.Lock()
					successes++
					mu.Unlock()
				}
			}()
		}
		wg.Wait()
		if successes != 1 {
			t.Fatalf("successful claims=%d want 1", successes)
		}
	})

	t.Run("capacity and lease recovery", func(t *testing.T) {
		store := factory(t)
		now := time.Now().UTC()
		first := candidate("cap-one", "cap-key-one", "subject-a", now)
		second := candidate("cap-two", "cap-key-two", "subject-a", now)
		third := candidate("cap-three", "cap-key-three", "subject-b", now)
		for _, value := range []session.Session{first, second, third} {
			if _, _, err := store.CreateOrGet(context.Background(), value.IdempotencyKey, value); err != nil {
				t.Fatal(err)
			}
		}
		lease, err := store.ClaimAndReserve(context.Background(), first.ID, first.TicketDigest, now, limits(1, 1, time.Second))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := store.ClaimAndReserve(context.Background(), second.ID, second.TicketDigest, now, limits(2, 1, time.Minute)); !errors.Is(err, session.ErrCapacity) {
			t.Fatalf("subject capacity error=%v", err)
		}
		if _, err := store.ClaimAndReserve(context.Background(), third.ID, third.TicketDigest, now, limits(1, 5, time.Minute)); !errors.Is(err, session.ErrCapacity) {
			t.Fatalf("global capacity error=%v", err)
		}
		if _, err := store.ClaimAndReserve(context.Background(), third.ID, third.TicketDigest, now.Add(2*time.Second), limits(1, 5, time.Minute)); err != nil {
			t.Fatalf("expired lease did not recover: %v", err)
		}
		if err := store.CloseAndRelease(context.Background(), first.ID, lease.ID, "late close", now.Add(2*time.Second)); err != nil {
			t.Fatalf("late duplicate close: %v", err)
		}
	})

	t.Run("expired ticket tombstone", func(t *testing.T) {
		store := factory(t)
		now := time.Now().UTC()
		value := candidate("expired", "expired-key", "subject", now)
		value.TicketExpiresAt = now.Add(time.Second)
		if _, _, err := store.CreateOrGet(context.Background(), value.IdempotencyKey, value); err != nil {
			t.Fatal(err)
		}
		replay := candidate("replacement", value.IdempotencyKey, value.SubjectID, now.Add(2*time.Second))
		replay.RequestFingerprint = value.RequestFingerprint
		if _, _, err := store.CreateOrGet(context.Background(), value.IdempotencyKey, replay); !errors.Is(err, session.ErrFailedPrecondition) {
			t.Fatalf("expired replay error=%v", err)
		}
	})
}

func candidate(id, key, subject string, now time.Time) session.Session {
	ticket := fmt.Sprintf("ticket-%s", id)
	return session.Session{ID: id, IdempotencyKey: key, RequestFingerprint: sha256.Sum256([]byte("fingerprint-" + key)), TicketDigest: sha256.Sum256([]byte(ticket)), TicketCiphertext: []byte("encrypted-" + id), TenantID: "tenant", SubjectID: subject, InstanceID: "instance", WorkloadName: "workload", WorkloadKind: session.WorkloadContainer, Mode: session.ModeExec, Command: []string{"/bin/sh"}, State: session.StateIssued, CreatedAt: now, TicketExpiresAt: now.Add(time.Minute), TombstoneExpiresAt: now.Add(15 * time.Minute)}
}

func limits(global, subject int, duration time.Duration) session.ClaimLimits {
	return session.ClaimLimits{MaxActive: global, MaxActivePerSubject: subject, SessionMaxDuration: duration, IdempotencyTTL: 15 * time.Minute}
}
