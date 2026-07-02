package ring

import (
	"sync"
	"time"

	"go.uber.org/zap"
)

// AffinityTable provides session-affinity routing for stateful services.
//
// ─── WHY SESSION AFFINITY IS SEPARATE FROM THE H&A RING ─────────────────────
// The H&A ring adjusts virtual node assignments based on load.  When a vnode
// moves from backend A to backend B, every request whose hash falls in that
// token range is now routed to B.  For stateless services this is fine.
//
// For stateful services (auth sessions, WebSocket connections, database
// transactions, gRPC streams), the session state lives on the backend.  Moving
// the hash range invalidates every in-flight session: 401 errors, empty carts,
// dropped WebSocket frames, aborted transactions.  These failures are invisible
// in aggregate metrics — they appear only as individual user errors.
//
// Fix: maintain a separate, immutable affinity table per session lifetime.
// The affinity table overrides the ring for sessions it knows about.  New
// sessions with no affinity entry fall through to the ring (and are registered
// once a backend is selected).  The ring serves new sessions; the affinity
// table serves existing ones.
//
// The table is immutable per session: once registered, the backend assignment
// never changes for that session.  Vnode adjustments do not affect the affinity
// table.  Sessions expire via TTL or explicit Expire() call.
//
// Usage pattern (proxy layer):
//
//	// Incoming request
//	sessionKey := extractSessionID(req)    // cookie, JWT sub, gRPC metadata, etc.
//	if backendID, ok := affinity.Route(sessionKey); ok {
//	    return ring.RouteToSpecific(backendID) // sticky: bypass ring hash
//	}
//	// New session — use ring
//	backendID, _ := ring.Route(hash)
//	affinity.Register(sessionKey, backendID)
//
// Classifying services:
//   - Stateless (REST APIs, read queries, cached responses): do not use affinity.
//   - Stateful (auth, WebSocket, DB connections, gRPC streams): use affinity.
//     Set Stateful=true on Backend when registering.
type AffinityTable struct {
	mu       sync.RWMutex
	sessions map[string]affinityRecord
	ttl      time.Duration
	maxSize  int // prevent unbounded memory growth
	log      *zap.Logger
}

type affinityRecord struct {
	backendID uint32
	createdAt time.Time
}

// NewAffinityTable creates a session affinity table.
// ttl is the session lifetime (default 30 minutes).
// maxSize is the maximum number of tracked sessions (default 1,000,000).
func NewAffinityTable(ttl time.Duration, maxSize int, log *zap.Logger) *AffinityTable {
	if ttl <= 0 {
		ttl = 30 * time.Minute
	}
	if maxSize <= 0 {
		maxSize = 1_000_000
	}
	return &AffinityTable{
		sessions: make(map[string]affinityRecord),
		ttl:      ttl,
		maxSize:  maxSize,
		log:      log,
	}
}

// Route returns the pinned backend for sessionKey, if one exists and has not expired.
// Returns (0, false) for unknown or expired sessions — caller should use the ring.
func (a *AffinityTable) Route(sessionKey string) (uint32, bool) {
	if sessionKey == "" {
		return 0, false
	}
	a.mu.RLock()
	rec, ok := a.sessions[sessionKey]
	a.mu.RUnlock()

	if !ok {
		return 0, false
	}
	if time.Since(rec.createdAt) > a.ttl {
		// Lazy expiry: don't block, just signal miss
		go a.Expire(sessionKey)
		return 0, false
	}
	return rec.backendID, true
}

// Register pins sessionKey to backendID for the duration of the TTL.
// If the table is at maxSize, the registration is silently dropped and the
// request falls through to ring routing (safe degradation, not a hard failure).
func (a *AffinityTable) Register(sessionKey string, backendID uint32) {
	if sessionKey == "" {
		return
	}
	a.mu.Lock()
	defer a.mu.Unlock()

	if len(a.sessions) >= a.maxSize {
		a.log.Warn("affinity table at capacity; new session will not be pinned",
			zap.Int("size", a.maxSize),
			zap.String("action", "session will use ring routing; increase maxSize if this persists"),
		)
		return
	}
	a.sessions[sessionKey] = affinityRecord{
		backendID: backendID,
		createdAt: time.Now(),
	}
}

// Expire removes a session from the affinity table.
// Call when the session ends (logout, connection close, TTL timeout).
// After Expire(), new requests with this session key will use the ring.
func (a *AffinityTable) Expire(sessionKey string) {
	a.mu.Lock()
	delete(a.sessions, sessionKey)
	a.mu.Unlock()
}

// Size returns the number of currently tracked sessions.
func (a *AffinityTable) Size() int {
	a.mu.RLock()
	defer a.mu.RUnlock()
	return len(a.sessions)
}

// GC removes all sessions whose TTL has elapsed.
// Run this in a background goroutine (e.g. every 5 minutes) to reclaim memory.
func (a *AffinityTable) GC() int {
	a.mu.Lock()
	defer a.mu.Unlock()

	now := time.Now()
	removed := 0
	for key, rec := range a.sessions {
		if now.Sub(rec.createdAt) > a.ttl {
			delete(a.sessions, key)
			removed++
		}
	}
	if removed > 0 {
		a.log.Debug("affinity table GC",
			zap.Int("expired_sessions", removed),
			zap.Int("remaining_sessions", len(a.sessions)),
		)
	}
	return removed
}

// ─── Ring manager integration ─────────────────────────────────────────────

// RouteStateful routes a request with session affinity: checks the affinity
// table first, falls back to the H&A ring for new sessions.
// sessionKey is the session identifier (cookie value, JWT sub, etc.).
// fallbackHash is the hash of the request key (source IP+port, etc.) used for
// ring routing when there is no existing affinity entry.
//
// If the backend pinned in the affinity table is no longer healthy, the entry
// is expired and the ring selects a new backend.  The caller must call
// Register() again with the new backend to re-establish affinity.
func (m *Manager) RouteStateful(sessionKey string, fallbackHash uint32) (uint32, bool, error) {
	if sessionKey != "" {
		if backendID, ok := m.affinity.Route(sessionKey); ok {
			// Verify backend is still healthy before honouring affinity
			m.mu.RLock()
			b, exists := m.backends[backendID]
			healthy := exists && b.Health
			m.mu.RUnlock()
			if healthy {
				return backendID, true, nil // affinity hit
			}
			// Backend is down: expire entry and fall through to ring
			m.affinity.Expire(sessionKey)
		}
	}

	// Affinity miss or expired: use ring
	id, err := m.Route(fallbackHash)
	return id, false, err // false = new session, caller should Register()
}
