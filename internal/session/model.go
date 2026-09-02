package session

import "time"

type State string

const (
	StateIssued  State = "issued"
	StateClaimed State = "claimed"
	StateClosed  State = "closed"
)

type Mode string

const (
	ModeExec   Mode = "exec"
	ModeSerial Mode = "serial"
	ModeVNC    Mode = "vnc"
)

type WorkloadKind string

const (
	WorkloadContainer    WorkloadKind = "container"
	WorkloadGPUContainer WorkloadKind = "gpu_container"
	WorkloadSandbox      WorkloadKind = "sandbox"
	WorkloadVM           WorkloadKind = "vm"
)

type TerminalSize struct{ Rows, Cols uint16 }

type Request struct {
	RequestID         string
	IdempotencyKey    string
	TenantID          string
	SubjectID         string
	InstanceID        string
	WorkloadName      string
	WorkloadKind      WorkloadKind
	Mode              Mode
	Container         string
	Command           []string
	TTY               bool
	Rows              uint16
	Cols              uint16
	RequestedProtocol string
}

type Session struct {
	ID                 string
	IdempotencyKey     string
	RequestFingerprint [32]byte
	TicketDigest       [32]byte
	TicketCiphertext   []byte
	TenantID           string
	SubjectID          string
	InstanceID         string
	WorkloadName       string
	WorkloadKind       WorkloadKind
	Mode               Mode
	Container          string
	Command            []string
	TTY                bool
	Rows               uint16
	Cols               uint16
	RequestedProtocol  string
	State              State
	CreatedAt          time.Time
	TicketExpiresAt    time.Time
	ExpiresAt          time.Time
	TombstoneExpiresAt time.Time
	ClaimedAt          time.Time
	ClosedAt           time.Time
	CloseReason        string
	LeaseID            string
}

type Issued struct {
	Session  Session
	Ticket   string
	Replayed bool
}

type ClaimLimits struct {
	MaxActive           int
	MaxActivePerSubject int
	SessionMaxDuration  time.Duration
	IdempotencyTTL      time.Duration
}

type SessionLease struct {
	ID        string
	Session   Session
	ExpiresAt time.Time
}
