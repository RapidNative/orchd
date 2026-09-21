package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"net/http"
	"sync"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// ErrReprovisionTimeout means the route for the host did not appear before
// the hold deadline — whether the reprovision webhook had already responded or
// was still running. The gateway maps this to 504 (distinct from a plain 404)
// so the client/CDN can retry; a reload a few seconds later usually succeeds.
var ErrReprovisionTimeout = errors.New("reprovision timed out waiting for route")

// reprovisionRequest is the body posted to the configured webhook when a host
// has no route. Type lets the receiver dispatch (future: workload.crashed, …).
type reprovisionRequest struct {
	Type string `json:"type"`
	Host string `json:"host"`
}

// reprovisionClient is a dedicated synchronous client. Unlike events.WebhookSink
// (fire-and-forget), this call waits for and acts on the response.
var reprovisionClient = &http.Client{Timeout: 30 * time.Second}

// RequestReprovision handles a gateway route-table miss for host. When a
// reprovision webhook is configured it notifies the receiver (e.g. the
// RapidNative Next.js API) to re-create the missing project, holds until the
// route reappears, and returns the resolved workload so the gateway can proxy
// the original request.
//
// It returns store.ErrNotFound when the feature is off or the receiver reports
// no such project (→ 404), and ErrReprovisionTimeout when the route never
// appears within the hold window (→ 504).
func (m *Manager) RequestReprovision(ctx context.Context, host string) (*store.Workload, error) {
	wh := m.GetWebhookConfig()
	if wh.URL == "" {
		return nil, store.ErrNotFound // feature off: behaves exactly like today
	}

	// Serialize concurrent misses for the same host so a burst fires one
	// webhook; the rest block here and then re-resolve below.
	hl := m.hostLock(host)
	hl.Lock()
	defer hl.Unlock()

	// A request that raced ahead may have already re-created the route.
	if wl, err := m.ResolveHost(host); err == nil {
		return wl, nil
	}

	// A canceled proxy request must not abort the reprovision it triggered.
	ctx = context.WithoutCancel(ctx)
	ctx, cancel := context.WithTimeout(ctx, m.cfg.ReprovisionHookTimeout)
	defer cancel()

	// Fire the hook and watch for the route in parallel. The receiver's
	// /projects/create registers the route within seconds and then keeps the
	// HTTP response open while it pushes files and attaches convention routes,
	// which regularly outlasts our hold window (Vercel allows it 60s; we hold
	// 30s). Waiting for the response before looking for the route turned that
	// into a 404 for the first visitor even though the project already existed
	// (109 occurrences in 7 days on staging). So: return as soon as the route
	// exists. The hook keeps its own lifetime — an early return must not cancel
	// the receiver's in-flight provisioning — and its outcome only matters if
	// the route never appears.
	hookDone := make(chan error, 1)
	go func() {
		hctx, hcancel := context.WithTimeout(context.WithoutCancel(ctx), m.cfg.ReprovisionHookTimeout)
		defer hcancel()
		hookDone <- m.postReprovisionHook(hctx, wh, host)
	}()

	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	hookPending := true
	for {
		if wl, err := m.ResolveHost(host); err == nil {
			return wl, nil
		}
		if hookPending {
			select {
			case err := <-hookDone:
				hookPending = false
				if err != nil {
					log.Printf("reprovision: hook for %s failed: %v", host, err)
					return nil, store.ErrNotFound
				}
				// Hook succeeded but the route is not visible yet: keep polling
				// until the deadline.
				continue
			case <-ctx.Done():
				log.Printf("reprovision: hook for %s still in flight at hold deadline; route not yet registered", host)
				return nil, ErrReprovisionTimeout
			case <-ticker.C:
			}
			continue
		}
		select {
		case <-ctx.Done():
			return nil, ErrReprovisionTimeout
		case <-ticker.C:
		}
	}
}

func (m *Manager) postReprovisionHook(ctx context.Context, wh store.Webhook, host string) error {
	body, _ := json.Marshal(reprovisionRequest{Type: "domain.not_found", Host: host})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if wh.APIKey != "" {
		req.Header.Set("X-Webhook-Key", wh.APIKey)
	}
	resp, err := reprovisionClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("reprovision hook returned %d", resp.StatusCode)
	}
	return nil
}

func (m *Manager) hostLock(host string) *sync.Mutex {
	m.mu.Lock()
	defer m.mu.Unlock()
	l, ok := m.hostLocks[host]
	if !ok {
		l = &sync.Mutex{}
		m.hostLocks[host] = l
	}
	return l
}
