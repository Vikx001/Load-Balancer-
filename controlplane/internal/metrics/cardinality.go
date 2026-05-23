// cardinality.go — Prometheus label cardinality guard for Omega-LB.
//
// ─── WHY CARDINALITY KILLS PROMETHEUS ────────────────────────────────────────
// Prometheus stores one time-series per unique label combination.  When labels
// include high-cardinality dimensions (backend_ip, path, user_id) the series
// count explodes exponentially:
//
//	10 backends × 200 paths × 50 status codes = 100,000 series
//
// At 15s scrape interval, this is 400k samples/min stored in-process.
// Prometheus OOMs at ~5M active series on a 16GB node; a single misbehaving
// label set can get there in minutes.
//
// ─── THE FIX ─────────────────────────────────────────────────────────────────
//  1. Per-dimension cardinality cap: once a dimension (e.g. "path") has seen
//     maxValues distinct label values, any new value is replaced with "_overflow".
//     This bounds series count at maxValues×(other dimensions).
//
//  2. Label set for per-request metrics:
//     ALLOWED:  backend_id, service_id, circuit_state_label
//     DISALLOWED as labels: backend_ip, path, user_id, request_id
//     → These go into exemplars (sampled traces) or structured logs, not labels.
//
//  3. Path aggregation: /api/v1/users/123 → /api/v1/users/{id}
//     Reduces path cardinality from O(user count) to O(route count).
//
// ─── OPERATIONAL COMMANDS ────────────────────────────────────────────────────
//
//	# See current cardinality:
//	$ curl http://localhost:9090/api/v1/label/__name__/values | jq '.data | length'
//	# Find high-cardinality series:
//	$ promtool tsdb analyze /var/lib/prometheus/data --extended
//	# Drop a bad series without full restart:
//	$ curl -X DELETE 'http://localhost:9090/api/v1/series?match[]=omega_lb_backend_latency_ms{path=~".+"}'
package metrics

import (
	"regexp"
	"sync"

	"go.uber.org/zap"
)

// CardinalityBudget enforces per-dimension label value caps.
// It is safe for concurrent use from the telemetry exporter goroutine.
type CardinalityBudget struct {
	mu        sync.Mutex
	counts    map[string]map[string]struct{} // dimension → set of allowed values
	maxVals   int
	log       *zap.Logger
	overflows uint64 // total overflow events (exported as a metric)
}

// NewCardinalityBudget creates a budget that allows at most maxVals distinct
// label values per dimension.  Use 0 to use the safe default (50).
func NewCardinalityBudget(maxVals int, log *zap.Logger) *CardinalityBudget {
	if maxVals <= 0 {
		maxVals = 50
	}
	return &CardinalityBudget{
		counts:  make(map[string]map[string]struct{}),
		maxVals: maxVals,
		log:     log,
	}
}

// Normalize returns value if it is within the cardinality budget for dimension,
// or "_overflow" if adding this value would exceed the cap.
//
// Usage:
//
//	label := budget.Normalize("path", rawPath)
//	// use label in Prometheus metric, not rawPath
func (cb *CardinalityBudget) Normalize(dimension, value string) string {
	cb.mu.Lock()
	defer cb.mu.Unlock()

	vals, ok := cb.counts[dimension]
	if !ok {
		vals = make(map[string]struct{})
		cb.counts[dimension] = vals
	}
	if _, seen := vals[value]; seen {
		return value // already known — allow
	}
	if len(vals) >= cb.maxVals {
		cb.overflows++
		if cb.overflows%1000 == 1 { // log once per 1000 overflows to avoid spam
			cb.log.Warn("metric cardinality budget exceeded; label value collapsed to _overflow",
				zap.String("dimension", dimension),
				zap.String("value", value),
				zap.Int("max_values", cb.maxVals),
				zap.String("action", "increase metrics.max_label_values or reduce label cardinality"),
			)
		}
		return "_overflow"
	}
	vals[value] = struct{}{}
	return value
}

// OverflowCount returns the total number of label values that were collapsed to
// "_overflow".  Expose this as a metric to alert on cardinality pressure.
func (cb *CardinalityBudget) OverflowCount() uint64 {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return cb.overflows
}

// DimensionSize returns the number of distinct values seen for a dimension.
func (cb *CardinalityBudget) DimensionSize(dimension string) int {
	cb.mu.Lock()
	defer cb.mu.Unlock()
	return len(cb.counts[dimension])
}

// ─── Path aggregation ──────────────────────────────────────────────────────

// pathIDPattern matches numeric or UUID-like path segments.
// Examples:
//
//	/api/v1/users/123         → /api/v1/users/{id}
//	/api/v1/orders/abc-def    → /api/v1/orders/{id}
//	/api/v1/orgs/42/items/7   → /api/v1/orgs/{id}/items/{id}
var pathIDPattern = regexp.MustCompile(
	`/([0-9a-fA-F]{8}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{4}-[0-9a-fA-F]{12}|[0-9]+)`,
)

// AggregatePath replaces numeric and UUID path segments with {id}.
// /api/v1/users/42/posts/7 → /api/v1/users/{id}/posts/{id}
// This collapses per-user/per-resource series into per-route series.
func AggregatePath(raw string) string {
	return pathIDPattern.ReplaceAllString(raw, "/{id}")
}
