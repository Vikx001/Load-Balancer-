package consensus

import (
	"context"
	"testing"
	"time"
)

func TestMemStateStoreGetMissingKeyReturnsZeroRevision(t *testing.T) {
	s := NewMemStateStore()
	val, rev, err := s.Get(context.Background(), "missing")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if val != nil || rev != 0 {
		t.Fatalf("expected (nil, 0) for missing key, got (%v, %d)", val, rev)
	}
}

func TestMemStateStorePutGetRoundTrip(t *testing.T) {
	s := NewMemStateStore()
	ctx := context.Background()

	if err := s.Put(ctx, "k", []byte("v1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	val, rev1, err := s.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(val) != "v1" || rev1 == 0 {
		t.Fatalf("expected (\"v1\", nonzero rev), got (%q, %d)", val, rev1)
	}

	if err := s.Put(ctx, "k", []byte("v2")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	val, rev2, err := s.Get(ctx, "k")
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if string(val) != "v2" {
		t.Fatalf("expected updated value \"v2\", got %q", val)
	}
	if rev2 <= rev1 {
		t.Fatalf("expected revision to increase monotonically: rev1=%d rev2=%d", rev1, rev2)
	}
}

func TestMemStateStorePutDoesNotAliasCallerBuffer(t *testing.T) {
	s := NewMemStateStore()
	ctx := context.Background()
	buf := []byte("original")
	if err := s.Put(ctx, "k", buf); err != nil {
		t.Fatalf("Put: %v", err)
	}
	buf[0] = 'X' // mutate caller's slice after Put returns

	val, _, _ := s.Get(ctx, "k")
	if string(val) != "original" {
		t.Fatalf("Put must copy the value, not alias it — got %q after caller mutated its buffer", val)
	}
}

func TestMemStateStoreWatchDeliversSubsequentPuts(t *testing.T) {
	s := NewMemStateStore()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := s.Watch(ctx, "k", 0)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}

	if err := s.Put(context.Background(), "k", []byte("hello")); err != nil {
		t.Fatalf("Put: %v", err)
	}

	select {
	case ev := <-ch:
		if string(ev.Value) != "hello" {
			t.Fatalf("expected watch event value \"hello\", got %q", ev.Value)
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for watch event")
	}
}

func TestMemStateStoreWatchClosesChannelOnContextCancel(t *testing.T) {
	s := NewMemStateStore()
	ctx, cancel := context.WithCancel(context.Background())

	ch, err := s.Watch(ctx, "k", 0)
	if err != nil {
		t.Fatalf("Watch: %v", err)
	}
	cancel()

	select {
	case _, ok := <-ch:
		if ok {
			t.Fatal("expected channel to be closed after context cancellation, got a value instead")
		}
	case <-time.After(time.Second):
		t.Fatal("timed out waiting for watch channel to close")
	}
}

func TestMemStateStoreTryLockAlwaysSucceeds(t *testing.T) {
	s := NewMemStateStore()
	acquired, cancel, err := s.TryLock(context.Background(), "leader", 10)
	if err != nil {
		t.Fatalf("TryLock: %v", err)
	}
	if !acquired {
		t.Fatal("MemStateStore.TryLock should always succeed (single-node, no contention)")
	}
	cancel()
}
