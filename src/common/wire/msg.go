package wire

import (
	"encoding/json"
	"fmt"
	"net"
	"sync"
	"time"
)

// MsgType identifies a frame.
type MsgType uint8

const (
	TAuth       MsgType = 1
	TWelcome    MsgType = 2
	TOfferRes   MsgType = 3
	TReconcile  MsgType = 4
	TGetTask    MsgType = 5
	TTask       MsgType = 6
	TTaskAccept MsgType = 7
	TTaskReject MsgType = 8
	THeartbeat  MsgType = 9
	TCancel     MsgType = 10
	TResult     MsgType = 11
	TResultAck  MsgType = 12
	TNotice     MsgType = 13
	TBye        MsgType = 14
	TChunk      MsgType = 15 // raw file data
	TFileEnd    MsgType = 16 // end of one file: JSON FileEnd
	TNoTask     MsgType = 17 // nothing to do right now
	TError      MsgType = 18
)

var names = map[MsgType]string{TAuth: "AUTH", TWelcome: "WELCOME", TOfferRes: "OFFER_RES",
	TReconcile: "RECONCILE", TGetTask: "GET_TASK", TTask: "TASK", TTaskAccept: "TASK_ACCEPT",
	TTaskReject: "TASK_REJECT", THeartbeat: "HEARTBEAT", TCancel: "CANCEL", TResult: "RESULT",
	TResultAck: "RESULT_ACK", TNotice: "NOTICE", TBye: "BYE", TChunk: "CHUNK", TFileEnd: "FILE_END",
	TNoTask: "NO_TASK", TError: "ERROR"}

func (t MsgType) String() string {
	if n, ok := names[t]; ok {
		return n
	}
	return fmt.Sprintf("type(%d)", uint8(t))
}

// Send marshals v as JSON and sends it.
func (c *Conn) Send(t MsgType, v any) error {
	b, err := marshal(v)
	if err != nil {
		return err
	}
	return c.WriteFrame(t, b)
}

func marshal(v any) ([]byte, error) { return json.Marshal(v) }

// Decode unmarshals a JSON payload.
func Decode(payload []byte, v any) error {
	return json.Unmarshal(payload, v)
}

// Expect reads one frame and requires the given type.
func (c *Conn) Expect(t MsgType, v any) error {
	got, p, err := c.ReadFrame()
	if err != nil {
		return err
	}
	if got == TError {
		var e ErrorMsg
		_ = Decode(p, &e)
		return fmt.Errorf("peer error: %s", e.Message)
	}
	if got != t {
		return fmt.Errorf("mwp: expected %v, got %v", t, got)
	}
	if v == nil {
		return nil
	}
	return Decode(p, v)
}

// ---------------------------------------------------------------------------------------
// Control messages

// Auth is the first message of an authenticated client.
type Auth struct {
	Agent     string `json:"agent"`                // program + version
	JoinToken string `json:"join_token,omitempty"` // register mode
	Name      string `json:"name,omitempty"`
	Platform  string `json:"platform,omitempty"`   // "linux-amd64": which release binary it needs
	CanUpdate bool   `json:"can_update,omitempty"` // it would install a signed release (D66)
}

// UpdateOffer is the message of a NOTICE of kind "update": a signed release to install.
// The client checks the signature itself (common/release): the server only relays it.
type UpdateOffer struct {
	URL      string `json:"url"`      // where the assets are (…/releases/latest/download)
	Manifest []byte `json:"manifest"` // release.json as signed
	Sig      []byte `json:"sig"`
}

// Welcome carries the session policy decided by the server (D11).
type Welcome struct {
	ServerID       string   `json:"server_id"`
	Status         string   `json:"status"` // "ok", "pending", "quarantined"
	HeartbeatSec   int      `json:"heartbeat_sec"`
	TaskTimeoutSec int      `json:"task_timeout_sec"`
	ReturnFiles    []string `json:"return_files"`      // glob patterns, "*" = everything
	ReturnExclude  []string `json:"return_exclude"`    // glob patterns
	MaxResultBytes int64    `json:"max_result_bytes"`  // -1 unlimited
	MetadataFields []string `json:"metadata_fields"`   // "*" = all
	Percentiles    []int    `json:"percentiles"`       // e.g. 50, 90, 99
	SampleSec      int      `json:"sample_sec"`        // telemetry sampling period
	OrcaVersions   []string `json:"orca_versions"`     // accepted "6.1.1@487d211c"
	OrcaTreeHashes []string `json:"orca_tree_hashes"`  // accepted installation fingerprints
	CompressLevel  int      `json:"compress_level"`    // deflate level for results
	KeepGBW        bool     `json:"keep_gbw"`          // verification needs .gbw files
	Message        string   `json:"message,omitempty"` // human-readable notice
	ServerUnixMs   int64    `json:"server_unix_ms"`    // for clock offset estimates
}

// OrcaInstall describes an ORCA installation found on a client.
type OrcaInstall struct {
	Version  string `json:"version"`   // "6.1.1"
	Git      string `json:"git"`       // "487d211c"
	TreeHash string `json:"tree_hash"` // SHA-256 over every executable (sorted)
	Path     string `json:"path"`
}

// OfferRes is the client capacity announcement.
type OfferRes struct {
	Slots        int           `json:"slots"`
	MemPerSlotMB int           `json:"mem_per_slot_mb"`
	ScratchFree  int64         `json:"scratch_free"`
	Orca         []OrcaInstall `json:"orca"`
	OS           string        `json:"os"`
	Arch         string        `json:"arch"`
	CPUModel     string        `json:"cpu_model"`
	Hostname     string        `json:"hostname,omitempty"`
	LogicalCPUs  int           `json:"logical_cpus"`
	BenchScore   float64       `json:"bench_score"` // ms for a fixed reference computation
	Accepting    bool          `json:"accepting"`   // false when paused by local schedule etc.
	PauseReason  string        `json:"pause_reason,omitempty"`
	MaxProcs     int           `json:"max_procs,omitempty"`    // largest MPI job accepted (0/1: single-core only)
	MemTotalMB   int           `json:"mem_total_mb,omitempty"` // memory pool shared by all jobs (0: fixed per slot)
	NoSecurity   bool          `json:"no_security,omitempty"`  // built without sandbox and audit (-tags nosecurity)
	MPI          string        `json:"mpi,omitempty"`          // "openmpi 4.1.8 <tree hash>"
}

// AttemptState is used in RECONCILE and HEARTBEAT.
type AttemptState struct {
	TaskID    string `json:"task_id"`
	AttemptID string `json:"attempt_id"`
	State     string `json:"state"` // running, finished, failed, lost
	Progress  int64  `json:"progress"`
	OutBytes  int64  `json:"out_bytes"`
	Tail      string `json:"tail,omitempty"` // last lines of the running job's output (live view)
}

// Reconcile is sent after every (re)connection.
type Reconcile struct {
	Attempts []AttemptState `json:"attempts"`
}

// GetTask asks for work.
type GetTask struct {
	Free      int `json:"free"`
	FreeMemMB int `json:"free_mem_mb,omitempty"` // left in the client's memory pool (memory_total)
}

// FileMeta describes a transferred file.
type FileMeta struct {
	Name   string `json:"name"` // relative, slash separated, sanitized by the receiver
	Size   int64  `json:"size"`
	SHA256 string `json:"sha256"`
}

// Task is an assignment. The input files follow as CHUNK/FILE_END sequences.
type Task struct {
	TaskID        string     `json:"task_id"`
	AttemptID     string     `json:"attempt_id"`
	InputName     string     `json:"input_name"` // e.g. "mol123.inp"
	Files         []FileMeta `json:"files"`
	LeaseSec      int        `json:"lease_sec"`
	Slots         int        `json:"slots"` // cores needed (1 for now)
	MemMB         int        `json:"mem_mb"`
	OrcaVersion   string     `json:"orca_version"`              // required "6.1.1"
	Kind          string     `json:"kind"`                      // "job" or "verify"
	MaxRuntimeSec int        `json:"max_runtime_sec,omitempty"` // tasks.max_job_time (0 = none): stop with Failure "time_limit"
}

// TaskReply accepts or rejects an assignment.
type TaskReply struct {
	AttemptID string `json:"attempt_id"`
	Reason    string `json:"reason,omitempty"`
}

// Heartbeat is sent every HeartbeatSec.
type Heartbeat struct {
	Attempts   []AttemptState `json:"attempts"`
	Load1      float64        `json:"load1"`
	CPUTempC   float64        `json:"cpu_temp_c"` // NaN not allowed in JSON; -1 = n/a
	FreeSlots  int            `json:"free_slots"`
	Accepting  bool           `json:"accepting"`
	SentUnix   int64          `json:"sent_unix_ms"`          // for RTT / clock offset estimates
	Power      string         `json:"power,omitempty"`       // "ac", "battery" or "none" (no system battery)
	BatteryPct float64        `json:"battery_pct,omitempty"` // -1 when there is no system battery
}

// Cancel stops an attempt.
type Cancel struct {
	AttemptID string `json:"attempt_id"`
	Reason    string `json:"reason"`
}

// Result announces a finished attempt; files follow.
type Result struct {
	AttemptID string     `json:"attempt_id"`
	TaskID    string     `json:"task_id"`
	ExitCode  int        `json:"exit_code"`
	Failure   string     `json:"failure,omitempty"` // "orca", "machine", "timeout", "safety", "returned" (the owner paused the computer), "time_limit" (tasks.max_job_time)
	Manifest  []byte     `json:"manifest"`          // signed manifest (see common/manifest)
	Files     []FileMeta `json:"files"`
}

// ResultAck confirms durable receipt.
type ResultAck struct {
	AttemptID string `json:"attempt_id"`
	Ledger    string `json:"ledger"` // ledger entry hash
	Verdict   string `json:"verdict"`
}

// Notice is a free-form message (warnings, drain requests, policy updates).
type Notice struct {
	Kind    string `json:"kind"` // "warn", "drain", "policy", "revoked", "info"
	Message string `json:"message"`
}

// FileEnd closes a file transfer.
type FileEnd struct {
	Name string `json:"name"`
}

// ErrorMsg reports a fatal protocol error before closing.
type ErrorMsg struct {
	Message string `json:"message"`
}

// ---------------------------------------------------------------------------------------
// Bandwidth limiting (client option, default 10 Mbit/s)

// RateLimiter is a simple token bucket shared by all writers.
type RateLimiter struct {
	mu       sync.Mutex
	rate     float64 // bytes per second
	burst    float64
	tokens   float64
	last     time.Time
	disabled bool
}

// NewRateLimiter creates a limiter; bitsPerSec <= 0 disables it.
func NewRateLimiter(bitsPerSec int64) *RateLimiter {
	if bitsPerSec <= 0 {
		return &RateLimiter{disabled: true}
	}
	r := float64(bitsPerSec) / 8
	return &RateLimiter{rate: r, burst: r / 4, tokens: r / 4, last: time.Now()}
}

// Wait blocks until n bytes may be sent.
func (l *RateLimiter) Wait(n int) {
	if l == nil || l.disabled {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	for {
		now := time.Now()
		l.tokens += now.Sub(l.last).Seconds() * l.rate
		l.last = now
		if l.tokens > l.burst {
			l.tokens = l.burst
		}
		if l.tokens >= float64(n) || (n > int(l.burst) && l.tokens >= l.burst) {
			l.tokens -= float64(n)
			return
		}
		need := (float64(n) - l.tokens) / l.rate
		if need > 0.25 {
			need = 0.25
		}
		time.Sleep(time.Duration(need * float64(time.Second)))
	}
}

type limitedConn struct {
	net.Conn
	l *RateLimiter
}

func (c *limitedConn) Write(b []byte) (int, error) {
	total := 0
	for len(b) > 0 {
		n := len(b)
		if n > 16<<10 {
			n = 16 << 10
		}
		c.l.Wait(n)
		w, err := c.Conn.Write(b[:n])
		total += w
		if err != nil {
			return total, err
		}
		b = b[n:]
	}
	return total, nil
}
