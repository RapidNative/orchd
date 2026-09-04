package manager

import (
	"testing"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

func TestShouldPersistActivity(t *testing.T) {
	now := time.Date(2026, 9, 3, 12, 0, 0, 0, time.UTC)
	win := 10 * time.Minute
	cases := []struct {
		name  string
		last  time.Time
		force bool
		want  bool
	}{
		{"force always persists", now.Add(-time.Second), true, true},
		{"never persisted before", time.Time{}, false, true},
		{"within window: coalesced", now.Add(-5 * time.Minute), false, false},
		{"exactly at window: persists", now.Add(-10 * time.Minute), false, true},
		{"past window: persists", now.Add(-11 * time.Minute), false, true},
	}
	for _, c := range cases {
		if got := shouldPersistActivity(c.last, now, win, c.force); got != c.want {
			t.Errorf("%s: got %v want %v", c.name, got, c.want)
		}
	}
}

func TestSetProjectLastActive_Monotonic(t *testing.T) {
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	m := New(config.Config{DataRoot: t.TempDir()}, st, reapStub{})
	_ = st.PutProject(&store.Project{ID: "p"})

	t1 := time.Date(2026, 8, 20, 0, 0, 0, 0, time.UTC)
	t0 := t1.Add(-48 * time.Hour)
	t2 := t1.Add(48 * time.Hour)

	get := func() time.Time { p, _ := st.GetProject("p"); return p.LastActiveAt }

	if err := m.SetProjectLastActive("p", t1); err != nil {
		t.Fatal(err)
	}
	if !get().Equal(t1) {
		t.Fatalf("want %v got %v", t1, get())
	}
	// Older value must NOT move the clock backwards (backfill can't clobber fresh).
	if err := m.SetProjectLastActive("p", t0); err != nil {
		t.Fatal(err)
	}
	if !get().Equal(t1) {
		t.Fatalf("backwards write leaked: want %v got %v", t1, get())
	}
	// Newer value advances it.
	if err := m.SetProjectLastActive("p", t2); err != nil {
		t.Fatal(err)
	}
	if !get().Equal(t2) {
		t.Fatalf("want %v got %v", t2, get())
	}
	// Unknown project errors (monotonic setter reports a missing project).
	if err := m.SetProjectLastActive("nope", t1); err == nil {
		t.Fatal("expected error for unknown project")
	}
}
