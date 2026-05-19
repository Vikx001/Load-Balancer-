package consensus

import (
	"context"
	"sync"
)

// MemStateStore is an in-memory implementation of StateStore for single-node
// deployments or testing.  It always acquires the leader lock (no contention)
// and delivers watch events in-process via channels.
//
// For multi-node deployments requiring real distributed consensus:
//
//	go get go.etcd.io/etcd/client/v3
//
// Then implement StateStore backed by clientv3.Client:
//   - Put:     client.Put(ctx, key, string(value))
//   - Get:     client.Get(ctx, key) → kv.Value, kv.ModRevision
//   - Watch:   client.Watch(ctx, key, clientv3.WithRev(startRevision))
//   - TryLock: concurrency.NewMutex(session, key).TryLock(ctx)
//   - Close:   client.Close()
type MemStateStore struct {
	mu       sync.RWMutex
	data     map[string][]byte
	revs     map[string]int64
	watchers map[string][]chan WatchEvent
	rev      int64
}

// NewMemStateStore returns an initialised in-memory state store.
func NewMemStateStore() *MemStateStore {
	return &MemStateStore{
		data:     make(map[string][]byte),
		revs:     make(map[string]int64),
		watchers: make(map[string][]chan WatchEvent),
	}
}

// Put writes key=value and notifies all watchers for that key.
func (m *MemStateStore) Put(_ context.Context, key string, value []byte) error {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.rev++
	cp := make([]byte, len(value))
	copy(cp, value)
	m.data[key] = cp
	m.revs[key] = m.rev
	ev := WatchEvent{Value: cp, Revision: m.rev}
	for _, ch := range m.watchers[key] {
		select {
		case ch <- ev:
		default:
			// slow consumer; skip rather than block the writer
		}
	}
	return nil
}

// Get returns the current value and revision for key.
// Returns revision=0 and value=nil if the key does not exist.
func (m *MemStateStore) Get(_ context.Context, key string) ([]byte, int64, error) {
	m.mu.RLock()
	defer m.mu.RUnlock()
	v, ok := m.data[key]
	if !ok {
		return nil, 0, nil
	}
	cp := make([]byte, len(v))
	copy(cp, v)
	return cp, m.revs[key], nil
}

// Watch returns a channel that delivers value updates for key.
// The channel is closed when ctx is cancelled.
// startRevision is accepted for interface compatibility but ignored in this
// implementation (all future events are delivered regardless of start revision).
func (m *MemStateStore) Watch(ctx context.Context, key string, _ int64) (<-chan WatchEvent, error) {
	ch := make(chan WatchEvent, 16)

	m.mu.Lock()
	m.watchers[key] = append(m.watchers[key], ch)
	m.mu.Unlock()

	// Remove this watcher and close the channel when ctx is cancelled.
	go func() {
		<-ctx.Done()
		m.mu.Lock()
		existing := m.watchers[key]
		updated := existing[:0]
		for _, w := range existing {
			if w != ch {
				updated = append(updated, w)
			}
		}
		m.watchers[key] = updated
		m.mu.Unlock()
		close(ch)
	}()

	return ch, nil
}

// TryLock always succeeds for MemStateStore (single-node: no contention).
// cancel releases the lock; ctx cancellation also releases it.
func (m *MemStateStore) TryLock(ctx context.Context, _ string, _ int64) (bool, context.CancelFunc, error) {
	_, cancel := context.WithCancel(ctx)
	return true, cancel, nil
}

// Close is a no-op for MemStateStore.
func (m *MemStateStore) Close() error { return nil }
