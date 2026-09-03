package manager

import (
	"context"
	"log"
	"sort"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// LRU eviction bounds how many projects stay resident on the box. Scale-to-zero
// (Suspend) frees compute but keeps a project's disk — containers, node_modules,
// caches, the database volume. On a box with thousands of rarely-touched projects
// that disk is the real ceiling. Eviction fully deletes the least-recently-active
// projects beyond Settings.LRUKeepMax; the reprovision-on-miss webhook re-creates
// one on its next request, so the fleet behaves as an LRU cache of projects.
//
// It is OFF unless an operator sets LRUKeepMax > 0 — orchd never deletes a project
// on its own otherwise.
//
// Eviction is destructive: a re-created project comes back with a FRESH database
// (recreation rebuilds code/routes from the caller's files, not the DB volume).
// It is meant for abandoned projects; snapshot-on-evict / restore-on-reprovision
// for losslessness is a deliberate later step, not this first cut.

// evictCandidate is the reaper's view of one project, decoupled from the store so
// the selection logic is pure and testable.
type evictCandidate struct {
	id         string
	lastActive time.Time
	protected  bool // keep_warm or currently running — never evict
}

// selectLRUEvictions returns the project ids to evict this tick: the oldest
// unprotected projects ranked beyond the newest `keep`, capped at `batch`, and
// never one active within `minAge`. Returns nil when eviction is disabled
// (keep <= 0) or nothing qualifies.
func selectLRUEvictions(cands []evictCandidate, keep, batch int, minAge time.Duration, now time.Time) []string {
	if keep <= 0 || batch <= 0 || len(cands) <= keep {
		return nil
	}
	// Most-recently-active first; the first `keep` are the protected working set.
	sort.Slice(cands, func(i, j int) bool { return cands[i].lastActive.After(cands[j].lastActive) })

	// Walk from the oldest upward, evicting unprotected candidates beyond `keep`.
	var evict []string
	for i := len(cands) - 1; i >= keep; i-- {
		c := cands[i]
		if c.protected {
			continue
		}
		if now.Sub(c.lastActive) < minAge {
			continue // active too recently — leave it regardless of rank
		}
		evict = append(evict, c.id)
		if len(evict) >= batch {
			break
		}
	}
	return evict
}

// ReapLRU evicts the least-recently-active projects beyond the configured ceiling.
// No-op when LRUKeepMax <= 0.
func (m *Manager) ReapLRU(ctx context.Context) {
	keep := m.GetLRUKeepMax()
	if keep <= 0 {
		return
	}
	projects := m.store.ListProjects()
	cands := make([]evictCandidate, 0, len(projects))
	for _, p := range projects {
		cands = append(cands, evictCandidate{
			id:         p.ID,
			lastActive: m.projectLastActive(p),
			protected:  m.projectProtected(p.ID),
		})
	}

	victims := selectLRUEvictions(cands, keep, m.cfg.LRUBatch, m.cfg.LRUMinAge, time.Now())
	if len(victims) == 0 {
		return
	}
	log.Printf("lru: %d projects resident > keep %d; evicting %d oldest (batch %d)",
		len(projects), keep, len(victims), m.cfg.LRUBatch)
	for _, id := range victims {
		la := time.Time{}
		if p, err := m.store.GetProject(id); err == nil {
			la = m.projectLastActive(p)
		}
		if err := m.DeleteProject(ctx, id); err != nil {
			log.Printf("lru: evict %s failed: %v", id, err)
			continue
		}
		log.Printf("lru: evicted %s (last active %s)", id, la.Format(time.RFC3339))
	}
}

// projectLastActive is the LRU key: LastActiveAt when set, else CreatedAt (so
// projects that predate the field sort by age rather than all-at-epoch).
func (m *Manager) projectLastActive(p *store.Project) time.Time {
	if !p.LastActiveAt.IsZero() {
		return p.LastActiveAt
	}
	return p.CreatedAt
}

// projectProtected reports whether any of the project's workloads is keep_warm or
// currently running — either makes the whole project ineligible for eviction.
func (m *Manager) projectProtected(projectID string) bool {
	for _, w := range m.store.ListWorkloads(projectID) {
		if w.KeepWarm || w.State == runtime.StateRunning {
			return true
		}
	}
	return false
}

// RunEvictionReaper runs ReapLRU on the configured interval until ctx is done.
func (m *Manager) RunEvictionReaper(ctx context.Context) {
	interval := m.cfg.LRUInterval
	if interval <= 0 {
		interval = 5 * time.Minute
	}
	t := time.NewTicker(interval)
	defer t.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-t.C:
			m.ReapLRU(ctx)
		}
	}
}
