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

// ErrReprovisionTimeout means the reprovision webhook responded but the route
// for the host did not appear before the hold deadline. The gateway maps this
// to 504 (distinct from a plain 404) so the client/CDN can retry.
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

	if err := m.postReprovisionHook(ctx, wh, host); err != nil {
		log.Printf("reprovision: hook for %s failed: %v", host, err)
		return nil, store.ErrNotFound
	}

	// The receiver re-creates the project synchronously (its /projects/create
	// registers the route before the async boot), so the route usually appears
	// immediately; poll to cover the small window. EnsureRunning then waits for
	// the boot itself.
	ticker := time.NewTicker(250 * time.Millisecond)
	defer ticker.Stop()
	for {
		if wl, err := m.ResolveHost(host); err == nil {
			return wl, nil
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
