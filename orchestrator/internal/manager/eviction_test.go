package manager

import (
	"context"
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

func TestSelectLRUEvictions(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	ago := func(d time.Duration) time.Time { return now.Add(-d) }
	minAge := 24 * time.Hour

	// 6 projects, oldest→newest by lastActive.
	base := []evictCandidate{
		{id: "p1", lastActive: ago(30 * 24 * time.Hour)},
		{id: "p2", lastActive: ago(20 * 24 * time.Hour)},
		{id: "p3", lastActive: ago(10 * 24 * time.Hour)},
		{id: "p4", lastActive: ago(5 * 24 * time.Hour)},
		{id: "p5", lastActive: ago(2 * 24 * time.Hour)},
		{id: "p6", lastActive: ago(1 * time.Hour)}, // within minAge
	}
	clone := func() []evictCandidate { c := make([]evictCandidate, len(base)); copy(c, base); return c }

	t.Run("disabled when keep<=0", func(t *testing.T) {
		if got := selectLRUEvictions(clone(), 0, 25, minAge, now); got != nil {
			t.Fatalf("keep=0 must evict nothing, got %v", got)
		}
	})

	t.Run("keep>=count evicts nothing", func(t *testing.T) {
		if got := selectLRUEvictions(clone(), 6, 25, minAge, now); got != nil {
			t.Fatalf("keep>=len must evict nothing, got %v", got)
		}
	})

	t.Run("keeps newest N, evicts oldest first", func(t *testing.T) {
		got := selectLRUEvictions(clone(), 3, 25, minAge, now)
		// keep p6,p5,p4 (newest 3); candidates p3,p2,p1; oldest first.
		want := []string{"p1", "p2", "p3"}
		assertEq(t, got, want)
	})

	t.Run("batch caps evictions", func(t *testing.T) {
		got := selectLRUEvictions(clone(), 2, 2, minAge, now)
		// candidates p4,p3,p2,p1 (oldest first p1,p2,p3,p4), cap 2 -> p1,p2
		assertEq(t, got, []string{"p1", "p2"})
	})

	t.Run("minAge protects a recently-active project even beyond keep", func(t *testing.T) {
		// keep 0 disabled; use keep 1 so p6 (newest) is kept anyway. Force p6 into
		// the candidate range by keeping only the 5 oldest: keep=5 keeps p6..p2,
		// candidate = p1 only. Instead test with a recent project ranked beyond keep:
		cands := []evictCandidate{
			{id: "old", lastActive: ago(40 * 24 * time.Hour)},
			{id: "recent", lastActive: ago(1 * time.Hour)}, // < minAge
			{id: "n1", lastActive: ago(2 * time.Hour)},
			{id: "n2", lastActive: ago(3 * time.Hour)},
		}
		// keep 1 -> keep "recent" (newest). candidates: n1,n2,old. None of n1/n2
		// are < minAge? n1=2h,n2=3h are < 24h -> protected by minAge; only "old" evicts.
		got := selectLRUEvictions(cands, 1, 25, minAge, now)
		assertEq(t, got, []string{"old"})
	})

	t.Run("protected (keep_warm/running) never evicted", func(t *testing.T) {
		cands := []evictCandidate{
			{id: "warm", lastActive: ago(50 * 24 * time.Hour), protected: true},
			{id: "a", lastActive: ago(10 * 24 * time.Hour)},
			{id: "b", lastActive: ago(5 * 24 * time.Hour)},
		}
		got := selectLRUEvictions(cands, 1, 25, minAge, now)
		// keep "b" (newest). candidates: a, warm(oldest). warm is protected -> only "a".
		assertEq(t, got, []string{"a"})
	})
}

func assertEq(t *testing.T, got, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("got %v, want %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
}

func TestReapLRU_EndToEnd(t *testing.T) {
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DataRoot: t.TempDir(), LRUBatch: 25, LRUMinAge: time.Hour, LRUInterval: time.Minute}
	m := New(cfg, st, reapStub{})

	now := time.Now()
	// p1 oldest .. p5 newest; p1 has a keep_warm workload (protected).
	mk := func(id string, daysAgo int, keepWarm bool) {
		_ = st.PutProject(&store.Project{ID: id, LastActiveAt: now.Add(-time.Duration(daysAgo) * 24 * time.Hour)})
		_ = st.PutWorkload(&store.Workload{ID: id + "-w", ProjectID: id, State: runtime.StateSuspended, KeepWarm: keepWarm})
	}
	mk("p1", 10, true) // protected
	mk("p2", 8, false)
	mk("p3", 6, false)
	mk("p4", 4, false)
	mk("p5", 2, false)

	// Disabled by default: no deletions.
	m.ReapLRU(context.Background())
	if len(st.ListProjects()) != 5 {
		t.Fatalf("keep_max=0 must evict nothing, have %d", len(st.ListProjects()))
	}

	// Keep newest 2 (p5,p4); candidates oldest-first p1,p2,p3; p1 protected -> evict p2,p3.
	if err := m.SetLRUKeepMax(2); err != nil {
		t.Fatal(err)
	}
	m.ReapLRU(context.Background())

	got := map[string]bool{}
	for _, p := range st.ListProjects() {
		got[p.ID] = true
	}
	for _, id := range []string{"p1", "p4", "p5"} {
		if !got[id] {
			t.Errorf("%s should remain (protected or within keep set)", id)
		}
	}
	for _, id := range []string{"p2", "p3"} {
		if got[id] {
			t.Errorf("%s should have been evicted", id)
		}
	}
	if len(got) != 3 {
		t.Fatalf("want 3 projects left, got %d (%v)", len(got), got)
	}
}
