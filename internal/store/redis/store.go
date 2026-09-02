package redisstore

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"strconv"
	"time"

	"github.com/zhangzhe-ctrl/ani-session-gateway/internal/session"
	"github.com/redis/go-redis/v9"
)

const defaultPrefix = "{ani-session-gateway}:"

type Store struct {
	client redis.UniversalClient
	prefix string
}

func New(client redis.UniversalClient) *Store { return &Store{client: client, prefix: defaultPrefix} }
func (s *Store) Mode() string                 { return "redis" }
func (s *Store) Ready(ctx context.Context) error {
	if err := s.client.Ping(ctx).Err(); err != nil {
		return fmt.Errorf("%w: %v", session.ErrUnavailable, err)
	}
	return nil
}

var createScript = redis.NewScript(`
local sid = redis.call('GET', KEYS[1])
if sid then
  local existing = ARGV[1] .. sid
  local fp = redis.call('HGET', existing, 'fingerprint')
  if not fp then return {3, sid} end
  if fp ~= ARGV[3] then return {2, sid} end
  local state = redis.call('HGET', existing, 'state')
  local ticket_exp = tonumber(redis.call('HGET', existing, 'ticket_expires_at') or '0')
  if state ~= 'issued' or tonumber(ARGV[2]) >= ticket_exp then
    if state == 'issued' then
      redis.call('HSET', existing, 'state', 'closed', 'closed_at', ARGV[2], 'close_reason', 'ticket_expired', 'ticket_ciphertext', '')
    end
    return {3, sid}
  end
  return {1, sid}
end
for i = 5, #ARGV - 1, 2 do redis.call('HSET', KEYS[2], ARGV[i], ARGV[i + 1]) end
redis.call('PEXPIREAT', KEYS[2], ARGV[#ARGV])
redis.call('SET', KEYS[1], ARGV[4], 'PXAT', ARGV[#ARGV])
return {0, ARGV[4]}
`)

var claimScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return {1} end
redis.call('ZREMRANGEBYSCORE', KEYS[2], '-inf', ARGV[1])
redis.call('ZREMRANGEBYSCORE', KEYS[3], '-inf', ARGV[1])
local state = redis.call('HGET', KEYS[1], 'state')
local ticket_exp = tonumber(redis.call('HGET', KEYS[1], 'ticket_expires_at') or '0')
if state ~= 'issued' or tonumber(ARGV[1]) >= ticket_exp then
  if state == 'issued' then redis.call('HSET', KEYS[1], 'state', 'closed', 'closed_at', ARGV[1], 'close_reason', 'ticket_expired', 'ticket_ciphertext', '') end
  return {3}
end
local stored = redis.call('HGET', KEYS[1], 'ticket_digest') or ''
local supplied = ARGV[2]
local different = 0
if string.len(stored) ~= string.len(supplied) then
  different = 1
else
  for i = 1, string.len(stored) do
    if string.byte(stored, i) ~= string.byte(supplied, i) then different = 1 end
  end
end
if different ~= 0 then return {2} end
if redis.call('ZCARD', KEYS[2]) >= tonumber(ARGV[3]) or redis.call('ZCARD', KEYS[3]) >= tonumber(ARGV[4]) then return {4} end
redis.call('HSET', KEYS[1], 'state', 'claimed', 'claimed_at', ARGV[1], 'expires_at', ARGV[5], 'lease_id', ARGV[6], 'ticket_ciphertext', '', 'tombstone_expires_at', ARGV[8])
redis.call('PEXPIREAT', KEYS[1], ARGV[8])
redis.call('PEXPIREAT', KEYS[4], ARGV[8])
redis.call('ZADD', KEYS[2], ARGV[5], ARGV[7])
redis.call('ZADD', KEYS[3], ARGV[5], ARGV[7])
return {0}
`)

var closeScript = redis.NewScript(`
if redis.call('EXISTS', KEYS[1]) == 0 then return {1} end
local state = redis.call('HGET', KEYS[1], 'state')
if state == 'closed' then return {0} end
if state ~= 'claimed' or redis.call('HGET', KEYS[1], 'lease_id') ~= ARGV[1] then return {2} end
redis.call('ZREM', KEYS[2], ARGV[4])
redis.call('ZREM', KEYS[3], ARGV[4])
redis.call('HSET', KEYS[1], 'state', 'closed', 'closed_at', ARGV[3], 'close_reason', ARGV[2], 'ticket_ciphertext', '')
return {0}
`)

func (s *Store) CreateOrGet(ctx context.Context, key string, candidate session.Session) (session.Session, bool, error) {
	fields, err := encode(candidate)
	if err != nil {
		return session.Session{}, false, err
	}
	args := []any{s.sessionPrefix(), candidate.CreatedAt.UnixMilli(), hex.EncodeToString(candidate.RequestFingerprint[:]), candidate.ID}
	for _, name := range fieldOrder {
		args = append(args, name, fields[name])
	}
	args = append(args, candidate.TombstoneExpiresAt.UnixMilli())
	result, err := createScript.Run(ctx, s.client, []string{s.idemKey(key), s.sessionKey(candidate.ID)}, args...).Slice()
	if err != nil {
		return session.Session{}, false, unavailable(err)
	}
	code, id, err := resultCodeID(result)
	if err != nil {
		return session.Session{}, false, err
	}
	switch code {
	case 2:
		return session.Session{}, false, session.ErrConflict
	case 3:
		return session.Session{}, false, session.ErrFailedPrecondition
	}
	stored, err := s.load(ctx, id)
	if err != nil {
		return session.Session{}, false, err
	}
	return stored, code == 1, nil
}

func (s *Store) ClaimAndReserve(ctx context.Context, id string, digest [32]byte, now time.Time, limits session.ClaimLimits) (session.SessionLease, error) {
	sessionKey := s.sessionKey(id)
	identity, err := s.client.HMGet(ctx, sessionKey, "subject_id", "idempotency_key").Result()
	if errors.Is(err, redis.Nil) {
		return session.SessionLease{}, session.ErrNotFound
	}
	if err != nil {
		return session.SessionLease{}, unavailable(err)
	}
	if len(identity) != 2 || identity[0] == nil || identity[1] == nil {
		return session.SessionLease{}, session.ErrNotFound
	}
	subject, subjectOK := identity[0].(string)
	idempotencyKey, keyOK := identity[1].(string)
	if !subjectOK || !keyOK {
		return session.SessionLease{}, session.ErrUnavailable
	}
	leaseID := id + ":lease"
	expires := now.Add(limits.SessionMaxDuration)
	tombstoneExpires := expires.Add(limits.IdempotencyTTL)
	result, err := claimScript.Run(ctx, s.client, []string{sessionKey, s.globalLeasesKey(), s.subjectLeasesKey(subject), s.idemKey(idempotencyKey)}, now.UnixMilli(), hex.EncodeToString(digest[:]), limits.MaxActive, limits.MaxActivePerSubject, expires.UnixMilli(), leaseID, id, tombstoneExpires.UnixMilli()).Slice()
	if err != nil {
		return session.SessionLease{}, unavailable(err)
	}
	code, err := singleCode(result)
	if err != nil {
		return session.SessionLease{}, err
	}
	switch code {
	case 1:
		return session.SessionLease{}, session.ErrNotFound
	case 2:
		return session.SessionLease{}, session.ErrInvalidTicket
	case 3:
		return session.SessionLease{}, session.ErrFailedPrecondition
	case 4:
		return session.SessionLease{}, session.ErrCapacity
	}
	stored, err := s.load(ctx, id)
	if err != nil {
		return session.SessionLease{}, err
	}
	return session.SessionLease{ID: leaseID, Session: stored, ExpiresAt: expires}, nil
}

func (s *Store) CloseAndRelease(ctx context.Context, id, leaseID, reason string, now time.Time) error {
	sessionKey := s.sessionKey(id)
	subject, err := s.client.HGet(ctx, sessionKey, "subject_id").Result()
	if errors.Is(err, redis.Nil) {
		return session.ErrNotFound
	}
	if err != nil {
		return unavailable(err)
	}
	result, err := closeScript.Run(ctx, s.client, []string{sessionKey, s.globalLeasesKey(), s.subjectLeasesKey(subject)}, leaseID, reason, now.UnixMilli(), id).Slice()
	if err != nil {
		return unavailable(err)
	}
	code, err := singleCode(result)
	if err != nil {
		return err
	}
	if code == 1 {
		return session.ErrNotFound
	}
	if code == 2 {
		return session.ErrFailedPrecondition
	}
	return nil
}

func (s *Store) load(ctx context.Context, id string) (session.Session, error) {
	fields, err := s.client.HGetAll(ctx, s.sessionKey(id)).Result()
	if err != nil {
		return session.Session{}, unavailable(err)
	}
	if len(fields) == 0 {
		return session.Session{}, session.ErrNotFound
	}
	return decode(fields)
}

func (s *Store) sessionPrefix() string       { return s.prefix + "session:" }
func (s *Store) sessionKey(id string) string { return s.sessionPrefix() + id }
func (s *Store) idemKey(key string) string {
	digest := sha256.Sum256([]byte(key))
	return s.prefix + "idem:" + hex.EncodeToString(digest[:])
}
func (s *Store) globalLeasesKey() string { return s.prefix + "leases" }
func (s *Store) subjectLeasesKey(subject string) string {
	digest := sha256.Sum256([]byte(subject))
	return s.prefix + "subject:" + hex.EncodeToString(digest[:]) + ":leases"
}

var fieldOrder = []string{"id", "idempotency_key", "fingerprint", "ticket_digest", "ticket_ciphertext", "tenant_id", "subject_id", "instance_id", "workload_name", "workload_kind", "mode", "container", "command", "tty", "rows", "cols", "requested_protocol", "state", "created_at", "ticket_expires_at", "expires_at", "tombstone_expires_at", "claimed_at", "closed_at", "close_reason", "lease_id"}

func encode(value session.Session) (map[string]string, error) {
	command, err := json.Marshal(value.Command)
	if err != nil {
		return nil, err
	}
	return map[string]string{
		"id": value.ID, "idempotency_key": value.IdempotencyKey, "fingerprint": hex.EncodeToString(value.RequestFingerprint[:]), "ticket_digest": hex.EncodeToString(value.TicketDigest[:]), "ticket_ciphertext": base64.RawStdEncoding.EncodeToString(value.TicketCiphertext),
		"tenant_id": value.TenantID, "subject_id": value.SubjectID, "instance_id": value.InstanceID, "workload_name": value.WorkloadName, "workload_kind": string(value.WorkloadKind), "mode": string(value.Mode), "container": value.Container, "command": string(command),
		"tty": strconv.FormatBool(value.TTY), "rows": strconv.Itoa(int(value.Rows)), "cols": strconv.Itoa(int(value.Cols)), "requested_protocol": value.RequestedProtocol, "state": string(value.State),
		"created_at": millis(value.CreatedAt), "ticket_expires_at": millis(value.TicketExpiresAt), "expires_at": millis(value.ExpiresAt), "tombstone_expires_at": millis(value.TombstoneExpiresAt), "claimed_at": millis(value.ClaimedAt), "closed_at": millis(value.ClosedAt), "close_reason": value.CloseReason, "lease_id": value.LeaseID,
	}, nil
}

func decode(fields map[string]string) (session.Session, error) {
	var value session.Session
	value.ID = fields["id"]
	value.IdempotencyKey = fields["idempotency_key"]
	value.TenantID = fields["tenant_id"]
	value.SubjectID = fields["subject_id"]
	value.InstanceID = fields["instance_id"]
	value.WorkloadName = fields["workload_name"]
	value.WorkloadKind = session.WorkloadKind(fields["workload_kind"])
	value.Mode = session.Mode(fields["mode"])
	value.Container = fields["container"]
	value.RequestedProtocol = fields["requested_protocol"]
	value.State = session.State(fields["state"])
	value.CloseReason = fields["close_reason"]
	value.LeaseID = fields["lease_id"]
	if err := decode32(fields["fingerprint"], &value.RequestFingerprint); err != nil {
		return value, err
	}
	if err := decode32(fields["ticket_digest"], &value.TicketDigest); err != nil {
		return value, err
	}
	if fields["ticket_ciphertext"] != "" {
		raw, err := base64.RawStdEncoding.DecodeString(fields["ticket_ciphertext"])
		if err != nil {
			return value, err
		}
		value.TicketCiphertext = raw
	}
	if err := json.Unmarshal([]byte(fields["command"]), &value.Command); err != nil {
		return value, err
	}
	value.TTY, _ = strconv.ParseBool(fields["tty"])
	rows, _ := strconv.ParseUint(fields["rows"], 10, 16)
	cols, _ := strconv.ParseUint(fields["cols"], 10, 16)
	value.Rows = uint16(rows)
	value.Cols = uint16(cols)
	for raw, dst := range map[string]*time.Time{"created_at": &value.CreatedAt, "ticket_expires_at": &value.TicketExpiresAt, "expires_at": &value.ExpiresAt, "tombstone_expires_at": &value.TombstoneExpiresAt, "claimed_at": &value.ClaimedAt, "closed_at": &value.ClosedAt} {
		*dst = parseMillis(fields[raw])
	}
	return value, nil
}

func decode32(raw string, target *[32]byte) error {
	decoded, err := hex.DecodeString(raw)
	if err != nil || len(decoded) != 32 {
		return errors.New("invalid stored digest")
	}
	copy(target[:], decoded)
	return nil
}
func millis(value time.Time) string {
	if value.IsZero() {
		return "0"
	}
	return strconv.FormatInt(value.UnixMilli(), 10)
}
func parseMillis(raw string) time.Time {
	value, _ := strconv.ParseInt(raw, 10, 64)
	if value == 0 {
		return time.Time{}
	}
	return time.UnixMilli(value).UTC()
}
func unavailable(err error) error { return fmt.Errorf("%w: %v", session.ErrUnavailable, err) }
func singleCode(values []any) (int64, error) {
	if len(values) == 0 {
		return 0, errors.New("empty Redis script result")
	}
	code, ok := values[0].(int64)
	if !ok {
		return 0, fmt.Errorf("invalid Redis script result %T", values[0])
	}
	return code, nil
}
func resultCodeID(values []any) (int64, string, error) {
	code, err := singleCode(values)
	if err != nil {
		return 0, "", err
	}
	if len(values) < 2 {
		return 0, "", errors.New("Redis script omitted session ID")
	}
	id, ok := values[1].(string)
	if !ok {
		return 0, "", fmt.Errorf("invalid Redis session ID %T", values[1])
	}
	return code, id, nil
}
