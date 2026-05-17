// Package health implements active + passive health checking (Layer 0 control plane).
package health

import (
	"context"
	"fmt"
	"net/http"
	"sync"
	"time"

	"go.uber.org/zap"

	"github.com/omega-lb/omega-lb/internal/config"
	"github.com/omega-lb/omega-lb/internal/ring"
)

// BackendEndpoint describes a health check target.
type BackendEndpoint struct {
	ID          uint32
	HealthURL   string // e.g. "http://10.0.0.1:8080/healthz"
	FailCount   int
	mu          sync.Mutex
}

// Checker runs active health probes and updates the H&A ring.
type Checker struct {
	cfg       config.HealthConfig
	ring      *ring.Manager
	log       *zap.Logger
	endpoints sync.Map // uint32 → *BackendEndpoint
	client    *http.Client
}

// NewChecker constructs the health checker.
func NewChecker(cfg config.HealthConfig, rm *ring.Manager, log *zap.Logger) *Checker {
	return &Checker{
		cfg:  cfg,
		ring: rm,
		log:  log,
		client: &http.Client{
			Timeout: 2 * time.Second,
			// No redirect following — health endpoints should not redirect
			CheckRedirect: func(req *http.Request, via []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}
}

// Register adds a backend endpoint for health checking.
func (c *Checker) Register(id uint32, healthURL string) {
	c.endpoints.Store(id, &BackendEndpoint{ID: id, HealthURL: healthURL})
}

// Deregister removes a backend from health checking.
func (c *Checker) Deregister(id uint32) {
	c.endpoints.Delete(id)
}

// Run is the main health check loop.
func (c *Checker) Run(ctx context.Context) error {
	ticker := time.NewTicker(time.Duration(c.cfg.ActiveIntervalS) * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-ticker.C:
			c.runChecks(ctx)
		}
	}
}

func (c *Checker) runChecks(ctx context.Context) {
	c.endpoints.Range(func(key, val any) bool {
		ep := val.(*BackendEndpoint)
		go c.check(ctx, ep)
		return true
	})
}

func (c *Checker) check(ctx context.Context, ep *BackendEndpoint) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, ep.HealthURL, nil)
	if err != nil {
		c.markFail(ep, fmt.Errorf("build request: %w", err))
		return
	}

	resp, err := c.client.Do(req)
	if err != nil {
		c.markFail(ep, err)
		return
	}
	resp.Body.Close()

	if resp.StatusCode >= 200 && resp.StatusCode < 300 {
		c.markPass(ep)
	} else {
		c.markFail(ep, fmt.Errorf("status %d", resp.StatusCode))
	}
}

func (c *Checker) markPass(ep *BackendEndpoint) {
	ep.mu.Lock()
	wasDown := ep.FailCount >= c.cfg.FailThreshold
	ep.FailCount = 0
	ep.mu.Unlock()

	if wasDown {
		c.ring.SetHealth(ep.ID, true)
		c.log.Info("backend recovered", zap.Uint32("id", ep.ID))
	}
}

func (c *Checker) markFail(ep *BackendEndpoint, err error) {
	ep.mu.Lock()
	ep.FailCount++
	failed := ep.FailCount >= c.cfg.FailThreshold
	ep.mu.Unlock()

	c.log.Warn("health check failed",
		zap.Uint32("id", ep.ID),
		zap.Error(err),
		zap.Int("consecutive_fails", ep.FailCount),
	)

	if failed {
		c.ring.SetHealth(ep.ID, false)
		c.log.Error("backend marked DOWN", zap.Uint32("id", ep.ID))
	}
}
