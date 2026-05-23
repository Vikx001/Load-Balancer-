package ring

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sync"

	"go.uber.org/zap"
)

// RingWAL is a file-backed write-ahead log for ring mutations.
//
// ─── WHY A WAL? ──────────────────────────────────────────────────────────────
// The Go daemon holds the H&A ring in memory.  The kernel reads routing
// decisions from eBPF maps.  These are two separate data structures that must
// stay in sync.  Without a WAL, a daemon crash between computing a ring update
// and pushing it to the eBPF map leaves the two permanently diverged:
//
//	Step 1: daemon computes new vnode assignment (ring updated in memory)
//	CRASH HERE — eBPF map still has old assignment
//	Step 2: daemon restarts with empty ring
//	→ eBPF map routes to X backends; daemon ring knows about Y backends
//
// Fix: write the intended mutation to the WAL (fsync) BEFORE applying it to
// the in-memory ring or the eBPF map.  On restart, replay any entries that
// were written but not yet committed (fsync again after eBPF map sync).
//
// The WAL is a newline-delimited JSON file.  Each line is a WALEntry.
// Committed entries are removed during the next Checkpoint() call.
//
// Write order:
//  1. WAL.Write(op)        — write + fsync
//  2. Apply to in-memory ring
//  3. Push to eBPF map
//  4. WAL.Commit(seq)      — mark committed; Checkpoint() when all done
type RingWAL struct {
	mu   sync.Mutex
	f    *os.File
	seq  uint64
	path string
	log  *zap.Logger
}

// WALEntry is a single ring mutation record.
type WALEntry struct {
	Seq       uint64 `json:"seq"`
	Op        string `json:"op"` // "set_vnode" | "add" | "remove"
	BackendID uint32 `json:"id"`
	Vnodes    int    `json:"vnodes,omitempty"`
	Committed bool   `json:"committed"`
}

// NewRingWAL opens (or creates) the WAL file at path.
// If the file already exists, it was left by a previous crashed instance;
// call Replay() to retrieve uncommitted entries and re-apply them.
func NewRingWAL(path string, log *zap.Logger) (*RingWAL, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, fmt.Errorf("open WAL %s: %w", path, err)
	}
	w := &RingWAL{f: f, path: path, log: log}
	// Determine the highest sequence number from existing entries.
	entries, _ := w.readAll()
	for _, e := range entries {
		if e.Seq > w.seq {
			w.seq = e.Seq
		}
	}
	return w, nil
}

// Write appends a mutation entry to the WAL with committed=false and fsyncs.
// Returns the sequence number that must be passed to Commit() after the eBPF
// map has been successfully updated.
func (w *RingWAL) Write(op string, backendID uint32, vnodes int) (uint64, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	w.seq++
	entry := WALEntry{
		Seq:       w.seq,
		Op:        op,
		BackendID: backendID,
		Vnodes:    vnodes,
		Committed: false,
	}
	line, err := json.Marshal(entry)
	if err != nil {
		return 0, fmt.Errorf("WAL marshal: %w", err)
	}
	if _, err = fmt.Fprintf(w.f, "%s\n", line); err != nil {
		return 0, fmt.Errorf("WAL write: %w", err)
	}
	if err = w.f.Sync(); err != nil {
		return 0, fmt.Errorf("WAL fsync: %w", err)
	}
	return w.seq, nil
}

// Commit marks the entry with the given sequence number as committed.
// After all pending entries are committed, Checkpoint() rewrites the file to
// remove committed entries and reclaim disk space.
func (w *RingWAL) Commit(seq uint64) error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entries, err := w.readAll()
	if err != nil {
		return fmt.Errorf("WAL read for commit: %w", err)
	}
	for i := range entries {
		if entries[i].Seq == seq {
			entries[i].Committed = true
		}
	}
	return w.rewrite(entries)
}

// Replay returns all uncommitted (pending) WAL entries.
// Called at daemon startup to re-apply any mutations that were written but
// not confirmed as pushed to the eBPF map before the previous crash.
func (w *RingWAL) Replay() ([]WALEntry, error) {
	w.mu.Lock()
	defer w.mu.Unlock()

	entries, err := w.readAll()
	if err != nil {
		return nil, err
	}
	var pending []WALEntry
	for _, e := range entries {
		if !e.Committed {
			pending = append(pending, e)
		}
	}
	if len(pending) > 0 {
		w.log.Warn("WAL replay: uncommitted entries found from previous crash",
			zap.Int("count", len(pending)),
		)
	}
	return pending, nil
}

// Checkpoint removes all committed entries from the WAL file.
// Call this after a batch of successful eBPF map updates.
func (w *RingWAL) Checkpoint() error {
	w.mu.Lock()
	defer w.mu.Unlock()

	entries, err := w.readAll()
	if err != nil {
		return err
	}
	var pending []WALEntry
	for _, e := range entries {
		if !e.Committed {
			pending = append(pending, e)
		}
	}
	return w.rewrite(pending)
}

// Close closes the underlying file.
func (w *RingWAL) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	return w.f.Close()
}

// readAll reads all WAL entries from the file (no lock; callers must hold mu).
func (w *RingWAL) readAll() ([]WALEntry, error) {
	if _, err := w.f.Seek(0, 0); err != nil {
		return nil, err
	}
	var entries []WALEntry
	scanner := bufio.NewScanner(w.f)
	for scanner.Scan() {
		line := scanner.Bytes()
		if len(line) == 0 {
			continue
		}
		var e WALEntry
		if err := json.Unmarshal(line, &e); err != nil {
			w.log.Warn("WAL: skipping malformed entry", zap.String("line", string(line)))
			continue
		}
		entries = append(entries, e)
	}
	return entries, scanner.Err()
}

// rewrite truncates the file and re-writes only the supplied entries (no lock).
func (w *RingWAL) rewrite(entries []WALEntry) error {
	if err := w.f.Truncate(0); err != nil {
		return fmt.Errorf("WAL truncate: %w", err)
	}
	if _, err := w.f.Seek(0, 0); err != nil {
		return err
	}
	for _, e := range entries {
		line, _ := json.Marshal(e)
		fmt.Fprintf(w.f, "%s\n", line) //nolint:errcheck // non-critical
	}
	return w.f.Sync()
}
