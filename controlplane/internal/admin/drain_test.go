package admin

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/config"
	"github.com/omega-lb/omega-lb/internal/ring"
)

func newTestServer(t *testing.T) (*Server, *ring.Manager) {
	t.Helper()
	rm, err := ring.NewManager(config.RingConfig{AdjustEveryN: 100, AdjustThreshold: 1.30}, zap.NewNop())
	if err != nil {
		t.Fatalf("ring.NewManager: %v", err)
	}
	rm.AddBackend(&ring.Backend{ID: 1, Health: true, CapacityMax: 1000})
	rm.AddBackend(&ring.Backend{ID: 2, Health: true, CapacityMax: 1000})
	return NewServer("127.0.0.1:0", nil, nil, rm, nil, "", zap.NewNop()), rm
}

func TestHandleDrainGetListsBackends(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/drain", nil)
	rec := httptest.NewRecorder()
	s.handleDrain(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var infos []drainBackendInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &infos); err != nil {
		t.Fatalf("decode: %v (%s)", err, rec.Body.String())
	}
	if len(infos) != 2 {
		t.Fatalf("expected 2 backends, got %d", len(infos))
	}
}

func TestHandleDrainPostSetsDrainingAndRingReflectsIt(t *testing.T) {
	s, rm := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/drain", strings.NewReader(`{"backend_id":1,"draining":true}`))
	rec := httptest.NewRecorder()
	s.handleDrain(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp drainBackendInfo
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Draining || resp.BackendID != 1 {
		t.Fatalf("unexpected response: %+v", resp)
	}

	b := rm.BackendInfo(1)
	if b == nil || !b.Draining {
		t.Fatalf("ring manager did not reflect drain state: %+v", b)
	}

	// The admin handler and the ring the proxy actually routes against are
	// the same *ring.Manager — confirm draining really blocks new routing,
	// not just the JSON response.
	for i := 0; i < 50; i++ {
		id, err := rm.Route(uint32(i))
		if err != nil {
			t.Fatalf("Route: %v", err)
		}
		if id == 1 {
			t.Fatalf("Route selected draining backend 1 after admin drain call")
		}
	}
}

func TestHandleDrainPostUnknownBackend(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/drain", strings.NewReader(`{"backend_id":999,"draining":true}`))
	rec := httptest.NewRecorder()
	s.handleDrain(rec, req)

	if rec.Code != http.StatusNotFound {
		t.Fatalf("expected 404, got %d: %s", rec.Code, rec.Body.String())
	}
}

func TestHandleDrainAllDrainsEveryBackend(t *testing.T) {
	s, rm := newTestServer(t)
	req := httptest.NewRequest(http.MethodPost, "/admin/drain/all", strings.NewReader(`{"draining":true}`))
	rec := httptest.NewRecorder()
	s.handleDrainAll(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("expected 200, got %d: %s", rec.Code, rec.Body.String())
	}
	var resp drainAllResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if !resp.Draining || resp.BackendCount != 2 {
		t.Fatalf("unexpected response: %+v", resp)
	}

	for _, id := range []uint32{1, 2} {
		if b := rm.BackendInfo(id); b == nil || !b.Draining {
			t.Fatalf("backend %d not draining after /admin/drain/all: %+v", id, b)
		}
	}
}

func TestHandleDrainAllMethodNotAllowed(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodGet, "/admin/drain/all", nil)
	rec := httptest.NewRecorder()
	s.handleDrainAll(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}

func TestHandleDrainMethodNotAllowed(t *testing.T) {
	s, _ := newTestServer(t)
	req := httptest.NewRequest(http.MethodDelete, "/admin/drain", nil)
	rec := httptest.NewRecorder()
	s.handleDrain(rec, req)

	if rec.Code != http.StatusMethodNotAllowed {
		t.Fatalf("expected 405, got %d", rec.Code)
	}
}
