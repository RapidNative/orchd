package manager

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// The wake check: before the gateway wakes a workload whose image is older than
// the newest built image of its template, it asks the configured webhook first
// (event "workload.image_behind") and waits for the answer, bounded by
// WakeHookTimeout. The receiver can bring the workload along while it is still
// asleep: write files to its disk, move it to the new image (PUT
// /v1/workloads/{id}/image). The wake then boots whatever the workload says.
//
// Why: a workload's image is fixed when its project is created. When a new
// image ships for a template (a runtime or SDK upgrade, say), a sleeping
// workload woke on its old image with its old files, and the only way to move
// it was to wake the whole fleet. The comparison is local and costs nothing for
// a workload that is current; the receiver is called only for one that is
// behind.
//
// The answer never blocks the wake: a failed, slow or negative reply starts the
// workload as it is. Each project is asked once per newest version: a receiver
// that answered and left the workload behind (it declined) is not asked again
// until a newer image ships. A failed or timed-out call is retried after
// wakeCheckCooldown. Concurrent wakes of one project (its app and its API)
// share one call.

const wakeCheckCooldown = 10 * time.Minute

type imageBehindEvent struct {
	Type          string `json:"type"`
	ProjectID     string `json:"project_id"`
	WorkloadID    string `json:"workload_id"`
	Template      string `json:"template"`
	ImageVersion  string `json:"image_version"`
	LatestVersion string `json:"latest_version"`
}

type wakeCheck struct {
	done     chan struct{}
	at       time.Time
	answered bool // guarded by m.mu
}

var wakeCheckClient = &http.Client{Timeout: 60 * time.Second}

// CheckImageBeforeWake runs the wake check for a workload the gateway is about
// to wake. It returns once the receiver has answered, the timeout passed, or
// there was nothing to ask. A live workload, one not booted from a built image,
// or one on its template's newest image returns at once, without a runtime
// call: the gateway runs this on every proxied request.
func (m *Manager) CheckImageBeforeWake(ctx context.Context, workloadID string) {
	// A live entry whose instance died is EnsureRunning's to restart; asking the
	// runtime here would add a status call to every request.
	if m.cfg.WakeHookTimeout <= 0 || m.isLive(workloadID) {
		return
	}
	w, err := m.store.GetWorkload(workloadID)
	if err != nil || w.Template == "" || w.ImageVer == "" {
		return
	}
	latest, ok := m.latestImageVersion(w.Template)
	if !ok || atoiVer(latest) <= atoiVer(w.ImageVer) {
		return
	}
	wh := m.GetWebhookConfig()
	if wh.URL == "" {
		return
	}

	key := w.ProjectID + "@" + latest
	m.mu.Lock()
	c, seen := m.wakeChecks[key]
	if seen && !c.answered && time.Since(c.at) > wakeCheckCooldown {
		seen = false
	}
	if !seen {
		c = &wakeCheck{done: make(chan struct{}), at: time.Now()}
		m.wakeChecks[key] = c
	}
	m.mu.Unlock()
	if !seen {
		go func() {
			defer close(c.done)
			// A canceled proxy request must not cut the receiver off mid-write.
			hctx, cancel := context.WithTimeout(context.WithoutCancel(ctx), m.cfg.WakeHookTimeout)
			defer cancel()
			start := time.Now()
			if err := m.postImageBehind(hctx, wh, w, latest); err != nil {
				log.Printf("wake check: %s (%s %s -> %s): %v", w.ID, w.Template, w.ImageVer, latest, err)
				return
			}
			m.mu.Lock()
			c.answered = true
			m.mu.Unlock()
			log.Printf("wake check: %s (%s %s -> %s) answered in %v", w.ID, w.Template, w.ImageVer, latest, time.Since(start).Round(time.Millisecond))
		}()
	}
	select {
	case <-c.done:
	case <-time.After(m.cfg.WakeHookTimeout):
	}
}

func (m *Manager) postImageBehind(ctx context.Context, wh store.Webhook, w *store.Workload, latest string) error {
	body, _ := json.Marshal(imageBehindEvent{
		Type:          "workload.image_behind",
		ProjectID:     w.ProjectID,
		WorkloadID:    w.ID,
		Template:      w.Template,
		ImageVersion:  w.ImageVer,
		LatestVersion: latest,
	})
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, wh.URL, bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	if wh.APIKey != "" {
		req.Header.Set("X-Webhook-Key", wh.APIKey)
	}
	resp, err := wakeCheckClient.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return fmt.Errorf("receiver returned %d", resp.StatusCode)
	}
	return nil
}
