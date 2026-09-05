package ring

// SetVnodeCount updates the virtual node count for a backend and rebuilds the ring.
// Called by the RL agent to apply new routing weights.
func (m *Manager) SetVnodeCount(id uint32, count int) {
	m.mu.Lock()
	defer m.mu.Unlock()
	if b, ok := m.backends[id]; ok {
		if count < 1 {
			count = 1
		}
		b.VnodeCount = count
		m.rebuild()
	}
}

// Backends returns a snapshot of current backend IDs (for RL state collection).
func (m *Manager) Backends() []uint32 {
	m.mu.RLock()
	defer m.mu.RUnlock()
	ids := make([]uint32, 0, len(m.backends))
	for id := range m.backends {
		ids = append(ids, id)
	}
	return ids
}

// SetDraining marks a backend as draining (or cancels a drain). A draining
// backend is immediately excluded from the ring — no new requests are routed
// to it — while its existing in-flight requests continue to be tracked
// normally via ReleaseConn. Reports whether the backend was found.
func (m *Manager) SetDraining(id uint32, draining bool) bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	b, ok := m.backends[id]
	if !ok {
		return false
	}
	b.Draining = draining
	m.rebuild()
	return true
}
