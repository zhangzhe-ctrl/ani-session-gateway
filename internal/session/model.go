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
	InstanceBinding   InstanceBinding
	VMBinding         VMBinding
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
	InstanceBinding    InstanceBinding
	VMBinding          VMBinding
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

// ClaimedAccess is the operational projection of a claimed session. It keeps
// ticket, idempotency, and persistence details behind the Manager seam.
type ClaimedAccess struct {
	SessionID string
	LeaseID   string
	ExpiresAt time.Time
	Identity  Identity
	Target    Target
	Mode      Mode
	Exec      *ExecOptions
}

type Identity struct {
	TenantID   string
	SubjectID  string
	InstanceID string
}

type Target struct {
	InstanceBinding InstanceBinding
	VMBinding       VMBinding
	WorkloadName    string
	WorkloadKind    WorkloadKind
}

type InstanceBinding struct {
	Enabled                                                                                bool
	Version                                                                                int64
	ClusterID, Namespace, NotebookName, NotebookUID, PodName, PodUID, ContainerName, Shell string
}

// VMBinding is the frozen Resource identity. No guest readiness or address is
// part of console admission: a running installer must remain accessible.
type VMBinding struct {
	Enabled      bool
	TenantID     string
	VMID         string
	Version      int64
	ClusterID    string
	Namespace    string
	VMName       string
	VMUID        string
	VMIName      string
	VMIUID       string
	NamespaceUID string
}

type ReplayRequest struct {
	IdempotencyKey  string
	TenantID        string
	SubjectID       string
	InstanceID      string
	ExpectedVersion int64
}

type ExecOptions struct {
	Container string
	Command   []string
	TTY       bool
	Size      TerminalSize
}
