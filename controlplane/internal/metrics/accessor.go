package metrics

// mu field is exported so telemetry can lock it. Add accessor so external
// packages don't need to know the struct layout.

// RLock locks the stats for reading.
func (s *BackendStats) RLock() { s.mu.Lock() }

// RUnlock unlocks.
func (s *BackendStats) RUnlock() { s.mu.Unlock() }
