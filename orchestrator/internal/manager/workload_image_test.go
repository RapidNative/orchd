package manager

import (
	"context"
	"errors"
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

func imageTestManager(t *testing.T) (*Manager, store.Store, *runtime.MockDriver) {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	rt := runtime.NewMockDriver()
	m := New(config.Config{DataRoot: t.TempDir()}, st, rt)
	for _, v := range []string{"v1", "v2"} {
		if err := st.PutImage(&store.Image{
			Template:  "app",
			Version:   v,
			Dockers:   map[string]string{"mobile": "app-mobile:" + v},
			Workloads: []store.ImageWorkload{{Name: "", Kind: "node", Workspace: "mobile"}, {Name: "db", Kind: "tinbase", Workspace: "db"}},
		}); err != nil {
			t.Fatal(err)
		}
	}
	_ = st.PutImage(&store.Image{Template: "app", Version: "v3-db-only", Workloads: []store.ImageWorkload{{Name: "db", Kind: "tinbase", Workspace: "db"}}})
	_ = st.PutProject(&store.Project{ID: "p"})
	_ = st.PutWorkload(&store.Workload{ID: "w", ProjectID: "p", Type: runtime.WorkloadRapidNativeDev, Template: "app", ImageVer: "v1", Workspace: "mobile", DataDir: t.TempDir()})
	return m, st, rt
}

func imageOf(t *testing.T, st store.Store) string {
	t.Helper()
	w, err := st.GetWorkload("w")
	if err != nil {
		t.Fatal(err)
	}
	return w.ImageVer
}

// A fleet move must not boot sleeping workloads: the version is recorded and the
// docker driver recreates the container on its next wake.
func TestSetWorkloadImage_SuspendedStaysAsleep(t *testing.T) {
	m, st, rt := imageTestManager(t)
	applied, err := m.SetWorkloadImage(context.Background(), "w", "v2")
	if err != nil {
		t.Fatal(err)
	}
	if applied != "next_wake" {
		t.Fatalf("applied = %q, want next_wake", applied)
	}
	if got := imageOf(t, st); got != "v2" {
		t.Fatalf("image_version = %q, want v2", got)
	}
	if st, _ := rt.Status(context.Background(), "w"); st == runtime.StateRunning {
		t.Fatal("a suspended workload was booted")
	}
	if spec := m.specFor(mustWorkload(t, st)); spec.Image != "app-mobile:v2" {
		t.Fatalf("spec image = %q, want app-mobile:v2 (what the next wake runs)", spec.Image)
	}
}

func TestSetWorkloadImage_RunningIsRecreated(t *testing.T) {
	m, st, rt := imageTestManager(t)
	if _, err := rt.Create(context.Background(), m.specFor(mustWorkload(t, st))); err != nil {
		t.Fatal(err)
	}
	applied, err := m.SetWorkloadImage(context.Background(), "w", "v2")
	if err != nil {
		t.Fatal(err)
	}
	if applied != "restarted" {
		t.Fatalf("applied = %q, want restarted", applied)
	}
	if st, _ := rt.Status(context.Background(), "w"); st != runtime.StateRunning {
		t.Fatalf("state after move = %v, want running", st)
	}
}

func TestSetWorkloadImage_RefusesWhatCannotRun(t *testing.T) {
	m, st, _ := imageTestManager(t)
	if applied, err := m.SetWorkloadImage(context.Background(), "w", "v1"); err != nil || applied != "unchanged" {
		t.Fatalf("same version: applied=%q err=%v, want unchanged", applied, err)
	}
	for _, v := range []string{"v9", "v3-db-only"} {
		if _, err := m.SetWorkloadImage(context.Background(), "w", v); !errors.Is(err, ErrImageMismatch) {
			t.Fatalf("%s: err = %v, want ErrImageMismatch", v, err)
		}
	}
	if got := imageOf(t, st); got != "v1" {
		t.Fatalf("a refused move changed image_version to %q", got)
	}
	if _, err := m.SetWorkloadImage(context.Background(), "missing", "v2"); !errors.Is(err, store.ErrNotFound) {
		t.Fatalf("unknown workload: err = %v, want ErrNotFound", err)
	}
}

func mustWorkload(t *testing.T, st store.Store) *store.Workload {
	t.Helper()
	w, err := st.GetWorkload("w")
	if err != nil {
		t.Fatal(err)
	}
	return w
}
