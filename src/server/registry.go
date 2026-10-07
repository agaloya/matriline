package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/agaloya/matriline/common/ident"
)

// Client status values.
const (
	stActive      = "active"
	stPending     = "pending"
	stRevoked     = "revoked"
	stQuarantined = "quarantined"
	stDraining    = "draining"
	stDisabled    = "disabled" // switched off by the admin: the client cleans up and exits
)

// ClientRec is a known client.
type ClientRec struct {
	ID        string    `json:"id"`
	Name      string    `json:"name"`
	PubKey    []byte    `json:"pubkey"`
	Status    string    `json:"status"`
	Created   time.Time `json:"created"`
	LastSeen  time.Time `json:"last_seen"`
	LastAddr  string    `json:"last_addr"`
	Results   int       `json:"results"`    // accepted results
	Verified  int       `json:"verified"`   // results that passed an active check
	Failures  int       `json:"failures"`   // failed verification checks
	OrcaFails int       `json:"orca_fails"` // ORCA errors reported
	// recent events for the automatic rules (see Server.clientFault)
	FailTimes []time.Time `json:"fail_times,omitempty"` // failed verifications
	ErrTimes  []time.Time `json:"err_times,omitempty"`  // ORCA or machine errors
	ErrStreak int         `json:"err_streak,omitempty"` // errors since the last accepted result
	// re-verification after a quarantine (see Server.recheckClient)
	RecheckPending int `json:"recheck_pending,omitempty"`
	RecheckFailed  int `json:"recheck_failed,omitempty"`
	RecheckRound   int `json:"recheck_round,omitempty"`
	RecheckFresh   int `json:"recheck_fresh,omitempty"` // new results still to count (nothing old to re-check)
	// AutoReleased: when a quarantine last ended by itself. Quarantined again within
	// verify.quarantine_window, the client is not released by itself a second time: a
	// cheater honest on its few accepted results was released and quarantined again and
	// again (Windows adversarial round, copy attack). Only the admin releases it then.
	AutoReleased  time.Time   `json:"auto_released,omitempty"`
	NoAutoRelease bool        `json:"no_auto_release,omitempty"`
	LastFailure   time.Time   `json:"last_failure,omitempty"` // last failed verification (reputation)
	Hostname      string      `json:"hostname,omitempty"`     // as reported by the client (OFFER_RES)
	HostChanges   []time.Time `json:"host_changes,omitempty"` // when the reported hostname changed (twin detection)
	Displaced     []time.Time `json:"displaced,omitempty"`    // live sessions replaced from another address
	Note          string      `json:"note,omitempty"`
	// what it reported when it last connected, for updates (D66)
	Agent     string `json:"agent,omitempty"`
	Platform  string `json:"platform,omitempty"`
	CanUpdate bool   `json:"can_update,omitempty"`
}

// Registry stores clients and join tokens (state/clients.json).
type Registry struct {
	mu      sync.Mutex
	path    string
	Clients map[string]*ClientRec `json:"clients"`
	Tokens  map[string]time.Time  `json:"tokens"` // join token -> expiry (register mode)
	// Enroll holds the one-time enrollment tokens of issued credentials, by SHA-256 of the
	// token (the registry file never contains a usable token).
	Enroll map[string]EnrollToken `json:"enroll,omitempty"`
}

// EnrollToken lets one device enroll under Name until Expires (keys issue).
type EnrollToken struct {
	Name    string    `json:"name"`
	Expires time.Time `json:"expires"`
	Uses    int       `json:"uses,omitempty"` // enrollments left (0 = the classic single use)
	Used    int       `json:"used,omitempty"` // enrollments made so far (names name-1, name-2, ...)
}

func openRegistry(path string) (*Registry, error) {
	r := &Registry{path: path, Clients: map[string]*ClientRec{}, Tokens: map[string]time.Time{}, Enroll: map[string]EnrollToken{}}
	b, err := os.ReadFile(path)
	if os.IsNotExist(err) {
		return r, nil
	}
	if err != nil {
		return nil, err
	}
	if err := json.Unmarshal(b, r); err != nil {
		return nil, fmt.Errorf("%s: %v", path, err)
	}
	if r.Clients == nil {
		r.Clients = map[string]*ClientRec{}
	}
	if r.Tokens == nil {
		r.Tokens = map[string]time.Time{}
	}
	if r.Enroll == nil {
		r.Enroll = map[string]EnrollToken{}
	}
	return r, nil
}

// saveLocked persists the registry; caller holds mu.
func (r *Registry) saveLocked() error {
	b, err := json.MarshalIndent(r, "", " ")
	if err != nil {
		return err
	}
	return ident.WriteFileAtomic(r.path, b, 0o600)
}

func (r *Registry) get(id string) *ClientRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	if c, ok := r.Clients[id]; ok {
		cp := *c
		return &cp
	}
	return nil
}

func (r *Registry) update(id string, f func(*ClientRec)) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.Clients[id]
	if !ok {
		return fmt.Errorf("unknown client %s", id)
	}
	f(c)
	return r.saveLocked()
}

// rename gives client id a new name, checked under the lock: not another client's name or
// id, nothing that looks like an id ("ml1-..."), not only dots (code review).
func (r *Registry) rename(id, name string) (string, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	c, ok := r.Clients[id]
	if !ok {
		return "", fmt.Errorf("unknown client %s", id)
	}
	if strings.Trim(name, ".") == "" || strings.HasPrefix(name, "ml1-") {
		return "", fmt.Errorf("%q cannot be a client's name", name)
	}
	for _, o := range r.Clients {
		if o.ID != id && o.Status != stRevoked && (o.Name == name || o.ID == name || strings.HasPrefix(o.ID, name)) {
			return "", fmt.Errorf("another client is already called %q (or has that id)", name)
		}
	}
	old := c.Name
	c.Name = name
	return old, r.saveLocked()
}

func (r *Registry) add(c *ClientRec) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if _, ok := r.Clients[c.ID]; ok {
		return fmt.Errorf("client %s already registered", c.ID)
	}
	r.Clients[c.ID] = c
	return r.saveLocked()
}

// resolve finds a client by id, id prefix or name.
func (r *Registry) resolve(q string) (*ClientRec, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	var hits []*ClientRec
	for _, c := range r.Clients {
		if c.ID == q || c.Name == q {
			return c, nil
		}
		if strings.HasPrefix(c.ID, q) {
			hits = append(hits, c)
		}
	}
	switch len(hits) {
	case 1:
		return hits[0], nil
	case 0:
		return nil, fmt.Errorf("no client matches %q", q)
	}
	return nil, fmt.Errorf("%q is ambiguous (%d clients)", q, len(hits))
}

func (r *Registry) list() []ClientRec {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]ClientRec, 0, len(r.Clients))
	for _, c := range r.Clients {
		out = append(out, *c)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].Name < out[j].Name })
	return out
}

func (r *Registry) newToken(ttl time.Duration) (string, error) {
	b := make([]byte, 18)
	rand.Read(b)
	tok := "mlj-" + hex.EncodeToString(b)
	r.mu.Lock()
	defer r.mu.Unlock()
	r.Tokens[tok] = time.Now().Add(ttl)
	return tok, r.saveLocked()
}

// newEnrollToken creates a one-time enrollment token for name, valid for ttl.
func (r *Registry) newEnrollToken(name string, ttl time.Duration, uses int) (string, error) {
	b := make([]byte, 24)
	rand.Read(b)
	tok := "mle-" + hex.EncodeToString(b)
	r.mu.Lock()
	defer r.mu.Unlock()
	now := time.Now()
	for h, e := range r.Enroll { // forget expired ones
		if now.After(e.Expires) {
			delete(r.Enroll, h)
		}
	}
	e := EnrollToken{Name: name, Expires: now.Add(ttl)}
	if uses > 1 {
		e.Uses = uses
	}
	r.Enroll[tokenHash(tok)] = e
	return tok, r.saveLocked()
}

// useEnrollToken consumes an enrollment token; ok = false if unknown, used or expired.
func (r *Registry) useEnrollToken(tok string) (EnrollToken, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	h := tokenHash(tok)
	e, ok := r.Enroll[h]
	if !ok {
		return EnrollToken{}, false
	}
	if e.Uses > 1 {
		// a ticket for several computers: each enrollment gets its own key and name
		e.Used++
		e.Uses--
		r.Enroll[h] = e
		e.Name = fmt.Sprintf("%s-%d", e.Name, e.Used)
	} else {
		if e.Used > 0 { // last use of a multi-use ticket
			e.Name = fmt.Sprintf("%s-%d", e.Name, e.Used+1)
		}
		delete(r.Enroll, h)
	}
	_ = r.saveLocked()
	return e, time.Now().Before(e.Expires)
}

// cancelEnroll drops the unused enrollment tokens issued for name.
func (r *Registry) cancelEnroll(name string) int {
	r.mu.Lock()
	defer r.mu.Unlock()
	n := 0
	for h, e := range r.Enroll {
		if e.Name == name {
			delete(r.Enroll, h)
			n++
		}
	}
	if n > 0 {
		_ = r.saveLocked()
	}
	return n
}

// pendingEnroll reports whether an unexpired enrollment token exists for name.
func (r *Registry) pendingEnroll(name string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, e := range r.Enroll {
		if e.Name == name && time.Now().Before(e.Expires) {
			return true
		}
	}
	return false
}

func tokenHash(tok string) string {
	h := sha256.Sum256([]byte(tok))
	return hex.EncodeToString(h[:])
}

// useToken consumes a join token (one-time use).
func (r *Registry) useToken(tok string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	exp, ok := r.Tokens[tok]
	if !ok {
		return false
	}
	delete(r.Tokens, tok)
	_ = r.saveLocked()
	return time.Now().Before(exp)
}
