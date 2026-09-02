package session

import (
	"context"
	"errors"
	"time"
)

var (
	ErrInvalidRequest     = errors.New("invalid session request")
	ErrConflict           = errors.New("idempotency key conflicts with request")
	ErrFailedPrecondition = errors.New("session is no longer claimable")
	ErrInvalidTicket      = errors.New("invalid session ticket")
	ErrCapacity           = errors.New("session capacity exhausted")
	ErrNotFound           = errors.New("session not found")
	ErrUnavailable        = errors.New("session store unavailable")
)

type Store interface {
	CreateOrGet(context.Context, string, Session) (Session, bool, error)
	ClaimAndReserve(context.Context, string, [32]byte, time.Time, ClaimLimits) (SessionLease, error)
	CloseAndRelease(context.Context, string, string, string, time.Time) error
	Ready(context.Context) error
	Mode() string
}
