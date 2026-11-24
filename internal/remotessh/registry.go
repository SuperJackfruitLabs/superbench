package remotessh

import (
	"sync"
	"time"

	"forge.superjackfruit.com/SuperJackfruitLabs/superbench/internal/remoteca"
)

// Registry holds the server's connections: the counts the pre-auth and
// connection limits check, every authenticated connection by ID with its
// bench, key fingerprint, claims and deadline, and each bench's open session
// channels. One Registry serves one listener and the renewal hook.
type Registry struct {
	mu       sync.Mutex
	next     uint64
	total    int // accepted connections not yet closed
	unauth   int // of those, connections still in the handshake
	conns    map[uint64]*conn
	sessions map[string]int // bench ID -> open session channels
}

func NewRegistry() *Registry {
	return &Registry{conns: map[uint64]*conn{}, sessions: map[string]int{}}
}

// admitConn counts a new connection, or refuses it when either the
// connection or the unauthenticated limit is reached. Nothing proportional to
// the client's input is allocated before this returns true.
func (r *Registry) admitConn() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.total >= MaxConnections || r.unauth >= MaxUnauthenticated {
		return false
	}
	r.total++
	r.unauth++
	return true
}

// leaveHandshake moves a counted connection out of the unauthenticated count.
func (r *Registry) leaveHandshake() {
	r.mu.Lock()
	r.unauth--
	r.mu.Unlock()
}

// dropConn forgets a counted connection.
func (r *Registry) dropConn(handshaking bool) {
	r.mu.Lock()
	r.total--
	if handshaking {
		r.unauth--
	}
	r.mu.Unlock()
}

func (r *Registry) add(c *conn) {
	r.mu.Lock()
	r.next++
	c.id = r.next
	r.conns[c.id] = c
	r.mu.Unlock()
}

func (r *Registry) remove(c *conn) {
	r.mu.Lock()
	delete(r.conns, c.id)
	r.mu.Unlock()
}

func (r *Registry) snapshot() []*conn {
	r.mu.Lock()
	defer r.mu.Unlock()
	out := make([]*conn, 0, len(r.conns))
	for _, c := range r.conns {
		out = append(out, c)
	}
	return out
}

// reserveSession takes one of a bench's MaxSessionsPerBench session slots.
func (r *Registry) reserveSession(benchID string) bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[benchID] >= MaxSessionsPerBench {
		return false
	}
	r.sessions[benchID]++
	return true
}

func (r *Registry) releaseSession(benchID string) {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.sessions[benchID] <= 1 {
		delete(r.sessions, benchID)
		return
	}
	r.sessions[benchID]--
}

// Extend moves the deadline of every open connection authenticated by this
// key fingerprint with exactly these claims to until, when until is later.
// A renewed certificate never revives a closed connection, and a connection
// whose claims differ (another lease, generation or execution) is untouched.
func (r *Registry) Extend(fingerprint string, c remoteca.Claims, until time.Time) {
	if fingerprint == "" {
		return
	}
	for _, x := range r.snapshot() {
		if x.fingerprint == fingerprint && x.claims == c {
			x.extend(until)
		}
	}
}
