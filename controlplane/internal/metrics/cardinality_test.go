package metrics

import (
	"testing"

	"go.uber.org/zap"
)

func TestNormalizeAllowsUpToMaxValues(t *testing.T) {
	b := NewCardinalityBudget(2, zap.NewNop())
	if got := b.Normalize("backend_id", "1"); got != "1" {
		t.Fatalf("got %q", got)
	}
	if got := b.Normalize("backend_id", "2"); got != "2" {
		t.Fatalf("got %q", got)
	}
	// Third distinct value exceeds the budget of 2.
	if got := b.Normalize("backend_id", "3"); got != "_overflow" {
		t.Fatalf("expected _overflow for the 3rd distinct value, got %q", got)
	}
	// Previously-seen values remain their real value even after overflow starts.
	if got := b.Normalize("backend_id", "1"); got != "1" {
		t.Fatalf("expected already-known value to stay as-is, got %q", got)
	}
}

func TestNormalizeDefaultsMaxValsWhenNonPositive(t *testing.T) {
	b := NewCardinalityBudget(0, zap.NewNop())
	if b.maxVals != 50 {
		t.Fatalf("expected default maxVals=50, got %d", b.maxVals)
	}
	b2 := NewCardinalityBudget(-5, zap.NewNop())
	if b2.maxVals != 50 {
		t.Fatalf("expected default maxVals=50 for negative input, got %d", b2.maxVals)
	}
}

func TestNormalizeDimensionsAreIndependent(t *testing.T) {
	b := NewCardinalityBudget(1, zap.NewNop())
	if got := b.Normalize("path", "a"); got != "a" {
		t.Fatalf("got %q", got)
	}
	if got := b.Normalize("method", "GET"); got != "GET" {
		t.Fatalf("a different dimension must have its own independent budget, got %q", got)
	}
}

func TestOverflowCountIncrementsOnlyOnOverflow(t *testing.T) {
	b := NewCardinalityBudget(1, zap.NewNop())
	b.Normalize("d", "a")
	if b.OverflowCount() != 0 {
		t.Fatalf("expected 0 overflows before the budget is exceeded")
	}
	b.Normalize("d", "b")
	b.Normalize("d", "c")
	if b.OverflowCount() != 2 {
		t.Fatalf("expected 2 overflows, got %d", b.OverflowCount())
	}
}

func TestDimensionSize(t *testing.T) {
	b := NewCardinalityBudget(10, zap.NewNop())
	b.Normalize("d", "a")
	b.Normalize("d", "b")
	b.Normalize("d", "a") // repeat — must not double-count
	if got := b.DimensionSize("d"); got != 2 {
		t.Fatalf("expected DimensionSize=2, got %d", got)
	}
	if got := b.DimensionSize("unknown"); got != 0 {
		t.Fatalf("expected DimensionSize=0 for an unseen dimension, got %d", got)
	}
}

func TestAggregatePath(t *testing.T) {
	cases := map[string]string{
		"/api/v1/users/123":                                  "/api/v1/users/{id}",
		"/api/v1/orgs/42/items/7":                            "/api/v1/orgs/{id}/items/{id}",
		"/api/v1/users/550e8400-e29b-41d4-a716-446655440000": "/api/v1/users/{id}",
		"/healthz": "/healthz",
	}
	for in, want := range cases {
		if got := AggregatePath(in); got != want {
			t.Errorf("AggregatePath(%q) = %q, want %q", in, got, want)
		}
	}
}

// TestAggregatePathDoesNotMatchNonUUIDAlphanumericIDs documents a real gap
// between pathIDPattern's doc comment (which gives "/api/v1/orders/abc-def
// → /api/v1/orders/{id}" as an example) and its actual behavior: the regex
// only matches pure-digit or full-UUID segments, not arbitrary alphanumeric
// IDs. This pins current behavior so it isn't silently changed; if the
// doc comment's example is the intended behavior, the regex needs a third
// alternative (e.g. a general opaque-ID pattern), not just this test edited.
func TestAggregatePathDoesNotMatchNonUUIDAlphanumericIDs(t *testing.T) {
	in := "/api/v1/orders/abc-def"
	if got := AggregatePath(in); got != in {
		t.Fatalf("AggregatePath(%q) = %q; if this now equals \"/api/v1/orders/{id}\", pathIDPattern's doc comment and behavior are back in sync — update this test to assert that instead", in, got)
	}
}
