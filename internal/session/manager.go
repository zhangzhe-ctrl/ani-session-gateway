package session

import (
	"context"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/binary"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/url"
	"strings"
	"time"

	"go.opentelemetry.io/otel"
)

type ManagerConfig struct {
	PublicWSBaseURL     *url.URL
	TicketKey           [32]byte
	TicketTTL           time.Duration
	SessionMaxDuration  time.Duration
	IdempotencyTTL      time.Duration
	MaxActive           int
	MaxActivePerSubject int
	Now                 func() time.Time
	Random              io.Reader
	Observer            Observer
}

type Observer interface {
	SessionCreated(mode, result string)
	SessionClaimed(result string)
}

type Manager struct {
	store  Store
	config ManagerConfig
	aead   cipher.AEAD
}

func NewManager(store Store, config ManagerConfig) (*Manager, error) {
	if store == nil || config.PublicWSBaseURL == nil {
		return nil, errors.New("store and public WebSocket base URL are required")
	}
	if config.Now == nil {
		config.Now = time.Now
	}
	if config.Random == nil {
		config.Random = rand.Reader
	}
	if config.TicketTTL <= 0 || config.SessionMaxDuration <= 0 || config.IdempotencyTTL <= 0 || config.MaxActive <= 0 || config.MaxActivePerSubject <= 0 {
		return nil, errors.New("positive session limits are required")
	}
	block, err := aes.NewCipher(config.TicketKey[:])
	if err != nil {
		return nil, err
	}
	aead, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	return &Manager{store: store, config: config, aead: aead}, nil
}

func (m *Manager) Issue(ctx context.Context, request Request) (Issued, error) {
	ctx, span := otel.Tracer("github.com/zhangzhe-ctrl/ani-session-gateway/session").Start(ctx, "session_manager.issue")
	defer span.End()
	result := "error"
	defer func() {
		if m.config.Observer != nil {
			m.config.Observer.SessionCreated(string(request.Mode), result)
		}
		if result != "success" && result != "replayed" {
			slog.Warn("session_failed", "request_id", request.RequestID, "tenant_id", request.TenantID, "subject_id", request.SubjectID, "instance_id", request.InstanceID, "mode", request.Mode, "result", result)
		}
	}()
	if err := validateRequest(request); err != nil {
		result = "invalid"
		return Issued{}, err
	}
	now := m.config.Now().UTC()
	ticketBytes := make([]byte, 32)
	if _, err := io.ReadFull(m.config.Random, ticketBytes); err != nil {
		return Issued{}, fmt.Errorf("generate ticket: %w", err)
	}
	ticket := base64.RawURLEncoding.EncodeToString(ticketBytes)
	digest := sha256.Sum256([]byte(ticket))
	ciphertext, err := m.encrypt([]byte(ticket))
	if err != nil {
		return Issued{}, fmt.Errorf("encrypt ticket: %w", err)
	}
	id, err := randomID(m.config.Random)
	if err != nil {
		return Issued{}, fmt.Errorf("generate session ID: %w", err)
	}
	candidate := Session{
		ID: id, IdempotencyKey: request.IdempotencyKey, RequestFingerprint: Fingerprint(request), TicketDigest: digest, TicketCiphertext: ciphertext,
		TenantID: request.TenantID, SubjectID: request.SubjectID, InstanceID: request.InstanceID, WorkloadName: request.WorkloadName,
		WorkloadKind: request.WorkloadKind, Mode: request.Mode, Container: request.Container, Command: append([]string(nil), request.Command...),
		TTY: request.TTY, Rows: request.Rows, Cols: request.Cols, RequestedProtocol: request.RequestedProtocol, State: StateIssued,
		CreatedAt: now, TicketExpiresAt: now.Add(m.config.TicketTTL), TombstoneExpiresAt: now.Add(m.config.TicketTTL + m.config.IdempotencyTTL),
	}
	stored, replayed, err := m.store.CreateOrGet(ctx, request.IdempotencyKey, candidate)
	if err != nil {
		result = observeResult(err)
		return Issued{}, err
	}
	if replayed {
		plaintext, err := m.decrypt(stored.TicketCiphertext)
		if err != nil {
			result = "failed_precondition"
			return Issued{}, ErrFailedPrecondition
		}
		ticket = string(plaintext)
	}
	if replayed {
		result = "replayed"
		slog.Info("session_replayed", "request_id", request.RequestID, "session_id", stored.ID, "tenant_id", stored.TenantID, "subject_id", stored.SubjectID, "instance_id", stored.InstanceID, "mode", stored.Mode)
	} else {
		result = "success"
		slog.Info("session_issued", "request_id", request.RequestID, "session_id", stored.ID, "tenant_id", stored.TenantID, "subject_id", stored.SubjectID, "instance_id", stored.InstanceID, "mode", stored.Mode)
	}
	return Issued{Session: stored, Ticket: ticket, Replayed: replayed}, nil
}

func (m *Manager) ConnectURL(issued Issued) string {
	u := *m.config.PublicWSBaseURL
	u.Path = strings.TrimSuffix(u.Path, "/") + "/sessions/" + url.PathEscape(issued.Session.ID)
	query := u.Query()
	query.Set("ticket", issued.Ticket)
	u.RawQuery = query.Encode()
	return u.String()
}

func (m *Manager) Claim(ctx context.Context, sessionID, ticket string) (ClaimedAccess, error) {
	ctx, span := otel.Tracer("github.com/zhangzhe-ctrl/ani-session-gateway/session").Start(ctx, "session_manager.claim")
	defer span.End()
	digest := sha256.Sum256([]byte(ticket))
	lease, err := m.store.ClaimAndReserve(ctx, sessionID, digest, m.config.Now().UTC(), ClaimLimits{MaxActive: m.config.MaxActive, MaxActivePerSubject: m.config.MaxActivePerSubject, SessionMaxDuration: m.config.SessionMaxDuration, IdempotencyTTL: m.config.IdempotencyTTL})
	result := observeResult(err)
	if m.config.Observer != nil {
		m.config.Observer.SessionClaimed(result)
	}
	if err == nil {
		slog.Info("session_claimed", "session_id", lease.Session.ID, "tenant_id", lease.Session.TenantID, "subject_id", lease.Session.SubjectID, "instance_id", lease.Session.InstanceID, "mode", lease.Session.Mode)
	}
	if err != nil {
		return ClaimedAccess{}, err
	}
	access := ClaimedAccess{
		SessionID: lease.Session.ID,
		LeaseID:   lease.ID,
		ExpiresAt: lease.ExpiresAt,
		Identity: Identity{
			TenantID:   lease.Session.TenantID,
			SubjectID:  lease.Session.SubjectID,
			InstanceID: lease.Session.InstanceID,
		},
		Target: Target{
			WorkloadName: lease.Session.WorkloadName,
			WorkloadKind: lease.Session.WorkloadKind,
		},
		Mode: lease.Session.Mode,
	}
	if lease.Session.Mode == ModeExec {
		access.Exec = &ExecOptions{
			Container: lease.Session.Container,
			Command:   append([]string(nil), lease.Session.Command...),
			TTY:       lease.Session.TTY,
			Size:      TerminalSize{Rows: lease.Session.Rows, Cols: lease.Session.Cols},
		}
	}
	return access, nil
}

func (m *Manager) Close(ctx context.Context, sessionID, leaseID, reason string) error {
	ctx, span := otel.Tracer("github.com/zhangzhe-ctrl/ani-session-gateway/session").Start(ctx, "session_manager.close")
	defer span.End()
	return m.store.CloseAndRelease(ctx, sessionID, leaseID, reason, m.config.Now().UTC())
}

func (m *Manager) encrypt(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, m.aead.NonceSize())
	if _, err := io.ReadFull(m.config.Random, nonce); err != nil {
		return nil, err
	}
	return m.aead.Seal(nonce, nonce, plaintext, nil), nil
}
func (m *Manager) decrypt(ciphertext []byte) ([]byte, error) {
	if len(ciphertext) < m.aead.NonceSize() {
		return nil, errors.New("invalid ciphertext")
	}
	return m.aead.Open(nil, ciphertext[:m.aead.NonceSize()], ciphertext[m.aead.NonceSize():], nil)
}

func observeResult(err error) string {
	switch {
	case err == nil:
		return "success"
	case errors.Is(err, ErrInvalidRequest):
		return "invalid"
	case errors.Is(err, ErrInvalidTicket):
		return "invalid_ticket"
	case errors.Is(err, ErrNotFound):
		return "not_found"
	case errors.Is(err, ErrConflict):
		return "conflict"
	case errors.Is(err, ErrFailedPrecondition):
		return "failed_precondition"
	case errors.Is(err, ErrCapacity):
		return "capacity"
	case errors.Is(err, ErrUnavailable):
		return "unavailable"
	default:
		return "error"
	}
}

func randomID(source io.Reader) (string, error) {
	raw := make([]byte, 16)
	if _, err := io.ReadFull(source, raw); err != nil {
		return "", err
	}
	raw[6] = (raw[6] & 0x0f) | 0x40
	raw[8] = (raw[8] & 0x3f) | 0x80
	return fmt.Sprintf("%x-%x-%x-%x-%x", raw[0:4], raw[4:6], raw[6:8], raw[8:10], raw[10:]), nil
}

func validateRequest(r Request) error {
	if r.IdempotencyKey == "" || r.TenantID == "" || r.SubjectID == "" || r.InstanceID == "" || r.WorkloadName == "" {
		return fmt.Errorf("%w: idempotency key, principal, instance, and workload are required", ErrInvalidRequest)
	}
	switch r.Mode {
	case ModeExec:
		if r.WorkloadKind != WorkloadContainer && r.WorkloadKind != WorkloadGPUContainer && r.WorkloadKind != WorkloadSandbox {
			return fmt.Errorf("%w: exec requires a container workload", ErrInvalidRequest)
		}
		if r.Rows == 0 || r.Cols == 0 || r.Rows > 4096 || r.Cols > 4096 {
			return fmt.Errorf("%w: exec terminal size must be between 1 and 4096", ErrInvalidRequest)
		}
	case ModeSerial, ModeVNC:
		if r.WorkloadKind != WorkloadVM {
			return fmt.Errorf("%w: VM console requires a VM workload", ErrInvalidRequest)
		}
	default:
		return fmt.Errorf("%w: unsupported session mode", ErrInvalidRequest)
	}
	return nil
}

func Fingerprint(r Request) [32]byte {
	h := sha256.New()
	write := func(value string) {
		var length [4]byte
		binary.BigEndian.PutUint32(length[:], uint32(len(value)))
		_, _ = h.Write(length[:])
		_, _ = h.Write([]byte(value))
	}
	for _, value := range []string{r.TenantID, r.SubjectID, r.InstanceID, r.WorkloadName, string(r.WorkloadKind), string(r.Mode), r.Container} {
		write(value)
	}
	for _, command := range r.Command {
		write(command)
	}
	if r.TTY {
		write("true")
	} else {
		write("false")
	}
	write(fmt.Sprintf("%d", r.Rows))
	write(fmt.Sprintf("%d", r.Cols))
	write(r.RequestedProtocol)
	var result [32]byte
	copy(result[:], h.Sum(nil))
	return result
}
