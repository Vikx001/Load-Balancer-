// Package admin provides the HTTP admin API for Omega-LB.
//
// ─── WHY SREs NEED THIS DURING INCIDENTS ─────────────────────────────────────
// At 3am, paged for a latency spike, an SRE faces a black box:
//
//	"The load balancer is routing 60% of traffic to backend-3.
//	 Backend-3 is saturated.  Why?"
//
// Without explainability:
//   - The RL agent's decision log is in zap.Debug level — not visible at runtime
//   - The RL weight vector is in memory, unreachable without a debugger
//   - The only option is to kill the process, losing all routing state
//
// With this admin server:
//
//	$ curl http://localhost:9000/admin/explain/recent | jq '.[-1]'
//	→ { backend_id: 3, reason: "normal", vnodes_at_select: 122, probe_idx: 0 }
//	$ curl http://localhost:9000/admin/mode   # check current mode
//	$ curl -XPOST http://localhost:9000/admin/mode -d '{"mode":"ASSISTED"}'  # bypass RL
//	$ curl -XPOST http://localhost:9000/admin/mode -d '{"mode":"MANUAL","weights":[0.33,0.33,0.34]}'
//	$ curl -XPOST http://localhost:9000/admin/drain -d '{"backend_id":3,"draining":true}'  # before a restart
//	$ curl http://localhost:9000/admin/healthz  # confirm daemon is alive
//
// ─── SECURITY NOTE ───────────────────────────────────────────────────────────
// This server MUST be bound to a private/loopback interface (:9000 by default).
// Set admin.token in config (or the OMEGA_ADMIN_TOKEN env var) to require a
// "X-Omega-Admin-Token: <token>" or "Authorization: Bearer <token>" header on
// every endpoint except /admin/healthz. If no token is configured, the API
// runs UNAUTHENTICATED and must NEVER be exposed beyond a trusted network.
// In Kubernetes: use a NetworkPolicy or an authenticated reverse proxy in
// addition to the token. In baremetal: bind to 127.0.0.1 or a management VPC
// NIC only.
package admin

import (
	"context"
	"crypto/subtle"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"time"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/consensus"
	"github.com/omega-lb/omega-lb/internal/observability"
	"github.com/omega-lb/omega-lb/internal/ring"
	"github.com/omega-lb/omega-lb/internal/rl"
)

// Server is the HTTP admin API server.
type Server struct {
	addr        string
	recorder    *observability.FlightRecorder
	agent       *rl.Agent
	ring        *ring.Manager
	coordinator *consensus.Coordinator
	token       string
	log         *zap.Logger
}

// NewServer creates an admin HTTP server.
// recorder, agent, ring, and coordinator may be nil if those subsystems are disabled.
// token, if non-empty, is required (via "X-Omega-Admin-Token" or "Authorization: Bearer"
// header) on every endpoint except /admin/healthz. Leaving token empty runs the API
// unauthenticated — only safe when bound to a loopback/private interface.
const (
	contentTypeJSON       = "application/json"
	defaultRecentHistory  = 100
	maxRecentHistory      = 1000
	defaultBackendHistory = 50
	maxBackendHistory     = 500
	adminTokenHeader      = "X-Omega-Admin-Token"
)

type apiError struct {
	Error string `json:"error"`
}

func NewServer(
	addr string,
	recorder *observability.FlightRecorder,
	agent *rl.Agent,
	rm *ring.Manager,
	coord *consensus.Coordinator,
	token string,
	log *zap.Logger,
) *Server {
	return &Server{
		addr:        addr,
		recorder:    recorder,
		agent:       agent,
		ring:        rm,
		coordinator: coord,
		token:       token,
		log:         log,
	}
}

// requestToken extracts the admin token from either the dedicated header or
// a standard "Authorization: Bearer <token>" header.
func requestToken(r *http.Request) string {
	if t := r.Header.Get(adminTokenHeader); t != "" {
		return t
	}
	if auth := r.Header.Get("Authorization"); strings.HasPrefix(strings.ToLower(auth), "bearer ") {
		return strings.TrimSpace(auth[len("bearer "):])
	}
	return ""
}

// requireAuth wraps a handler so it 401s unless the caller presents a token
// matching s.token. Comparison is constant-time to avoid timing side-channels.
// If s.token is empty, auth is disabled and the handler runs unchanged.
func (s *Server) requireAuth(next http.HandlerFunc) http.HandlerFunc {
	if s.token == "" {
		return next
	}
	return func(w http.ResponseWriter, r *http.Request) {
		given := requestToken(r)
		if given == "" || subtle.ConstantTimeCompare([]byte(given), []byte(s.token)) != 1 {
			writeJSONError(w, http.StatusUnauthorized, "unauthorized: missing or invalid admin token")
			return
		}
		next(w, r)
	}
}

func writeJSON(w http.ResponseWriter, v interface{}) {
	w.Header().Set("Content-Type", contentTypeJSON)
	_ = json.NewEncoder(w).Encode(v)
}

func writeJSONError(w http.ResponseWriter, status int, message string) {
	w.WriteHeader(status)
	writeJSON(w, apiError{Error: message})
}

func (s *Server) checkMethod(w http.ResponseWriter, r *http.Request, allowed string) bool {
	if r.Method != allowed {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return false
	}
	return true
}

func parsePositiveInt(values url.Values, key string, defaultValue, maxValue int) int {
	if v := values.Get(key); v != "" {
		if parsed, err := strconv.Atoi(v); err == nil && parsed > 0 {
			if parsed > maxValue {
				return maxValue
			}
			return parsed
		}
	}
	return defaultValue
}

func parseUint32(values url.Values, key string) (uint32, error) {
	idStr := values.Get(key)
	if idStr == "" {
		return 0, fmt.Errorf("missing query param: %s", key)
	}
	idParsed, err := strconv.ParseUint(idStr, 10, 32)
	if err != nil {
		return 0, fmt.Errorf("invalid %s: must be uint32", key)
	}
	return uint32(idParsed), nil
}

// Run starts the admin HTTP server and blocks until ctx is cancelled.
func (s *Server) Run(ctx context.Context) error {
	mux := http.NewServeMux()
	mux.HandleFunc("/admin/healthz", s.handleHealthz)
	mux.HandleFunc("/admin/explain/recent", s.requireAuth(s.handleExplainRecent))
	mux.HandleFunc("/admin/explain/backend", s.requireAuth(s.handleExplainBackend))
	mux.HandleFunc("/admin/mode", s.requireAuth(s.handleMode))
	mux.HandleFunc("/admin/ring", s.requireAuth(s.handleRing))
	mux.HandleFunc("/admin/drain", s.requireAuth(s.handleDrain))
	mux.HandleFunc("/admin/consensus", s.requireAuth(s.handleConsensus))

	srv := &http.Server{
		Addr:         s.addr,
		Handler:      mux,
		ReadTimeout:  5 * time.Second,
		WriteTimeout: 10 * time.Second,
		IdleTimeout:  30 * time.Second,
	}

	errCh := make(chan error, 1)
	go func() { errCh <- srv.ListenAndServe() }()

	if s.token == "" {
		s.log.Warn("admin API running WITHOUT authentication — bind to a loopback/private interface only",
			zap.String("addr", s.addr),
		)
	}
	s.log.Info("admin API server started",
		zap.String("addr", s.addr),
		zap.Bool("authenticated", s.token != ""),
		zap.String("endpoints", "/admin/healthz /admin/explain/recent /admin/explain/backend /admin/mode /admin/ring /admin/drain /admin/consensus"),
	)

	select {
	case <-ctx.Done():
		shutCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		return srv.Shutdown(shutCtx)
	case err := <-errCh:
		return err
	}
}

// ─── GET /admin/healthz ───────────────────────────────────────────────────────

func (s *Server) handleHealthz(w http.ResponseWriter, r *http.Request) {
	if !s.checkMethod(w, r, http.MethodGet) {
		return
	}
	writeJSON(w, map[string]string{
		"status": "ok",
		"time":   time.Now().UTC().Format(time.RFC3339Nano),
	})
}

// ─── GET /admin/explain/recent?n=N ───────────────────────────────────────────
//
// Returns the last N routing decisions from the flight recorder.
// Use this to answer: "what has the LB been doing for the last 100ms?"
//
// Example:
//   $ curl 'http://localhost:9000/admin/explain/recent?n=10' | jq '.[] | {backend_id,reason}'

func (s *Server) handleExplainRecent(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
		return
	}
	n := parsePositiveInt(r.URL.Query(), "n", defaultRecentHistory, maxRecentHistory)
	if s.recorder == nil {
		http.Error(w, "flight recorder not available", http.StatusServiceUnavailable)
		return
	}
	decisions := s.recorder.Recent(n)
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(decisions)
}

// ─── GET /admin/explain/backend?id=N&n=N ─────────────────────────────────────
//
// Returns recent routing decisions for a specific backend.
// Use this to answer: "why was backend-3 receiving so much traffic?"
//
// Example:
//   $ curl 'http://localhost:9000/admin/explain/backend?id=3&n=20' | jq '.'

func (s *Server) handleExplainBackend(w http.ResponseWriter, r *http.Request) {
	if !s.checkMethod(w, r, http.MethodGet) {
		return
	}
	if s.recorder == nil {
		http.Error(w, "flight recorder not available", http.StatusServiceUnavailable)
		return
	}
	id, err := parseUint32(r.URL.Query(), "id")
	if err != nil {
		http.Error(w, err.Error(), http.StatusBadRequest)
		return
	}
	n := parsePositiveInt(r.URL.Query(), "n", defaultBackendHistory, maxBackendHistory)
	decisions := s.recorder.ForBackend(id, n)
	writeJSON(w, decisions)
}

// ─── GET/POST /admin/mode ─────────────────────────────────────────────────────
//
// GET  — returns current mode and model version
// POST — switches mode; optional weights for MANUAL mode
//
// Request body (POST):
//   {"mode": "AUTO" | "ASSISTED" | "MANUAL", "weights": [0.5, 0.3, 0.2]}
//
// Modes:
//   AUTO      — full RL control (KAN actor + CBF + oscillation gate)
//   ASSISTED  — H&A ring only; KAN skipped; CBF still active (safe default under incident)
//   MANUAL    — operator-specified weights; use for exact traffic splits during maintenance
//
// Example:
//   $ curl -XPOST http://localhost:9000/admin/mode \
//       -H 'Content-Type: application/json' \
//       -d '{"mode":"MANUAL","weights":[0.5,0.3,0.2]}'

type modeRequest struct {
	Mode    string    `json:"mode"`
	Weights []float64 `json:"weights,omitempty"`
}

type modeResponse struct {
	Mode         string    `json:"mode"`
	ModelVersion string    `json:"model_version,omitempty"`
	EffectiveAt  time.Time `json:"effective_at"`
}

func (s *Server) handleMode(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		s.handleGetMode(w, r)
	case http.MethodPost:
		s.handleSetMode(w, r)
	default:
		http.Error(w, "method not allowed", http.StatusMethodNotAllowed)
	}
}

func (s *Server) handleGetMode(w http.ResponseWriter, r *http.Request) {
	resp := modeResponse{EffectiveAt: time.Now().UTC()}
	if s.agent != nil {
		resp.Mode = modeString(s.agent.GetMode())
		resp.ModelVersion = s.agent.GetModelVersion()
	} else {
		resp.Mode = "AGENT_DISABLED"
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func (s *Server) handleSetMode(w http.ResponseWriter, r *http.Request) {
	if s.agent == nil {
		http.Error(w, "RL agent not enabled", http.StatusServiceUnavailable)
		return
	}

	var req modeRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "invalid JSON body: "+err.Error(), http.StatusBadRequest)
		return
	}

	var mode rl.AgentMode
	switch req.Mode {
	case "AUTO":
		mode = rl.ModeAuto
	case "ASSISTED":
		mode = rl.ModeAssisted
	case "MANUAL":
		mode = rl.ModeManual
	default:
		http.Error(w, `invalid mode: must be "AUTO", "ASSISTED", or "MANUAL"`, http.StatusBadRequest)
		return
	}

	if mode == rl.ModeManual && len(req.Weights) == 0 {
		http.Error(w, `MANUAL mode requires "weights" array`, http.StatusBadRequest)
		return
	}

	s.agent.SetMode(mode, req.Weights)
	s.log.Info("admin: agent mode changed",
		zap.String("mode", req.Mode),
		zap.Float64s("weights", req.Weights),
	)

	resp := modeResponse{
		Mode:         req.Mode,
		ModelVersion: s.agent.GetModelVersion(),
		EffectiveAt:  time.Now().UTC(),
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

func modeString(m rl.AgentMode) string {
	switch m {
	case rl.ModeAuto:
		return "AUTO"
	case rl.ModeAssisted:
		return "ASSISTED"
	case rl.ModeManual:
		return "MANUAL"
	default:
		return "UNKNOWN"
	}
}

// ─── GET /admin/ring ──────────────────────────────────────────────────────────
//
// Returns current ring topology: all backends with vnode counts, weight shares,
// and health status.  Use this to answer "why is backend-2 getting 40% of traffic?"
//
// Example:
//   $ curl http://localhost:9000/admin/ring | jq '.backends[] | {name: .id, vnodes, weight_pct, healthy}'

type ringBackendInfo struct {
	ID         uint32  `json:"id"`
	IP         string  `json:"ip"`
	Port       uint16  `json:"port"`
	Vnodes     int     `json:"vnodes"`
	WeightPct  float64 `json:"weight_pct"` // percentage of total vnodes
	Healthy    bool    `json:"healthy"`
	Stateful   bool    `json:"stateful"`
	Draining   bool    `json:"draining"`
	ActiveReqs int64   `json:"active_reqs"`
}

type ringResponse struct {
	Backends     []ringBackendInfo `json:"backends"`
	TotalVnodes  int               `json:"total_vnodes"`
	BackendCount int               `json:"backend_count"`
}

func (s *Server) handleRing(w http.ResponseWriter, r *http.Request) {
	if !s.checkMethod(w, r, http.MethodGet) {
		return
	}
	if s.ring == nil {
		http.Error(w, "ring manager not available", http.StatusServiceUnavailable)
		return
	}

	ids := s.ring.Backends()
	total := 0 // vnodes actually in the ring — excludes unhealthy/draining backends
	backends := make([]ringBackendInfo, 0, len(ids))
	for _, id := range ids {
		b := s.ring.BackendInfo(id)
		if b == nil {
			continue
		}
		inRing := b.Health && !b.Draining
		if inRing {
			total += b.VnodeCount
		}
		backends = append(backends, ringBackendInfo{
			ID:         b.ID,
			IP:         fmt.Sprintf("%d.%d.%d.%d", b.IP[0], b.IP[1], b.IP[2], b.IP[3]),
			Port:       b.Port,
			Vnodes:     b.VnodeCount,
			Healthy:    b.Health,
			Stateful:   b.Stateful,
			Draining:   b.Draining,
			ActiveReqs: b.ActiveReqs,
		})
	}

	// Weight percentages reflect actual traffic share: an unhealthy or
	// draining backend gets 0%, even though its Vnodes field (its configured
	// count, restored as-is when it rejoins the ring) is left unchanged.
	for i := range backends {
		if !backends[i].Healthy || backends[i].Draining || total == 0 {
			continue
		}
		backends[i].WeightPct = float64(backends[i].Vnodes) / float64(total) * 100.0
	}

	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(ringResponse{
		Backends:     backends,
		TotalVnodes:  total,
		BackendCount: len(backends),
	})
}

// ─── GET/POST /admin/drain ───────────────────────────────────────────────────
//
// GET returns the draining state of every backend. POST marks a backend as
// draining (or cancels a drain) so an operator can gracefully take it out of
// rotation before a deploy/restart — no new request is routed to it, but its
// existing in-flight requests keep completing normally. Unlike an unhealthy
// backend, a draining one still passes health checks; it is withdrawn on
// purpose, not because it failed.
//
// Example:
//
//	$ curl http://localhost:9000/admin/drain | jq .
//	$ curl -XPOST http://localhost:9000/admin/drain -d '{"backend_id":3,"draining":true}'
//	→ {"backend_id":3,"draining":true,"active_reqs":12}
//	# poll active_reqs (via this endpoint or /admin/ring) until it reaches 0,
//	# then restart backend-3 and clear the drain:
//	$ curl -XPOST http://localhost:9000/admin/drain -d '{"backend_id":3,"draining":false}'

type drainRequest struct {
	BackendID uint32 `json:"backend_id"`
	Draining  bool   `json:"draining"`
}

type drainBackendInfo struct {
	BackendID  uint32 `json:"backend_id"`
	Draining   bool   `json:"draining"`
	ActiveReqs int64  `json:"active_reqs"`
}

func (s *Server) handleDrain(w http.ResponseWriter, r *http.Request) {
	if s.ring == nil {
		writeJSONError(w, http.StatusServiceUnavailable, "ring manager not available")
		return
	}
	switch r.Method {
	case http.MethodGet:
		s.handleGetDrain(w, r)
	case http.MethodPost:
		s.handleSetDrain(w, r)
	default:
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
	}
}

func (s *Server) handleGetDrain(w http.ResponseWriter, r *http.Request) {
	ids := s.ring.Backends()
	infos := make([]drainBackendInfo, 0, len(ids))
	for _, id := range ids {
		b := s.ring.BackendInfo(id)
		if b == nil {
			continue
		}
		infos = append(infos, drainBackendInfo{
			BackendID:  b.ID,
			Draining:   b.Draining,
			ActiveReqs: b.ActiveReqs,
		})
	}
	writeJSON(w, infos)
}

func (s *Server) handleSetDrain(w http.ResponseWriter, r *http.Request) {
	var req drainRequest
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}

	if !s.ring.SetDraining(req.BackendID, req.Draining) {
		writeJSONError(w, http.StatusNotFound, fmt.Sprintf("backend %d not found", req.BackendID))
		return
	}

	s.log.Info("admin: backend drain state changed",
		zap.Uint32("backend_id", req.BackendID),
		zap.Bool("draining", req.Draining),
	)

	resp := drainBackendInfo{BackendID: req.BackendID, Draining: req.Draining}
	if b := s.ring.BackendInfo(req.BackendID); b != nil {
		resp.ActiveReqs = b.ActiveReqs
	}
	writeJSON(w, resp)
}

// ─── GET /admin/consensus ─────────────────────────────────────────────────────
//
// Returns the consensus coordinator status: whether this node is the leader,
// the last ring state version it applied, and the store backend type.
//
// Example:
//   $ curl http://localhost:9000/admin/consensus | jq .
//   → {"node_id":"node-1","is_leader":true,"last_applied_version":1716000000000,"store_type":"memory"}

func (s *Server) handleConsensus(w http.ResponseWriter, r *http.Request) {
	if !s.checkMethod(w, r, http.MethodGet) {
		return
	}
	if s.coordinator == nil {
		http.Error(w, "consensus coordinator not enabled (stage < 3)", http.StatusServiceUnavailable)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(s.coordinator.GetStatus())
}
