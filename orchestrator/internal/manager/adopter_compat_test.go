package manager

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// These tests pin the engine's move to adopter config: with RapidNative's
// adopter file, everything RapidNative relied on comes out exactly as when the
// engine hard-coded it; without one, nothing of RapidNative's applies.

// formerCatalog is the preset table the engine used to hard-code.
var formerCatalog = map[string]preset{
	"tinbase": {Type: runtime.WorkloadTinbaseProject},
	"expo":    {Type: "rapidnative-dev", Name: "app", Image: "rn-expo:dev", Port: 8080},
	"vite":    {Type: "rapidnative-dev", Name: "web", Image: "rn-vite:dev", Port: 8080},
	"api":     {Type: "rapidnative-dev", Name: "api", Image: "rn-api:dev", Port: 8080},
}

func compatManager(t *testing.T, withRN bool) (*Manager, store.Store) {
	t.Helper()
	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{DataRoot: t.TempDir(), BaseDomain: "test.local", DevMemMB: 3072, DevCPUs: 2, TinbaseMemMB: 384, TinbaseCPUs: 0.5}
	if withRN {
		cfg.Adopter = rapidNativeAdopter(t)
	}
	return New(cfg, st, reapStub{}), st
}

func TestRapidNativePresetsMatchFormerCatalog(t *testing.T) {
	m, _ := compatManager(t, true)
	if got := m.presets(); !reflect.DeepEqual(got, formerCatalog) {
		t.Fatalf("presets = %+v\nwant      %+v", got, formerCatalog)
	}
	if got := m.PresetNames(); !reflect.DeepEqual(got, []string{"api", "expo", "tinbase", "vite"}) {
		t.Fatalf("PresetNames = %v", got)
	}
}

func TestNeutralEngineHasOnlyTheTinbasePreset(t *testing.T) {
	m, _ := compatManager(t, false)
	if got := m.PresetNames(); !reflect.DeepEqual(got, []string{"tinbase"}) {
		t.Fatalf("PresetNames = %v", got)
	}
}

// A workload created from each RapidNative preset is registered with the same
// type, name, image, port and default limits as before.
func TestRapidNativePresetWorkloadsUnchanged(t *testing.T) {
	m, _ := compatManager(t, true)
	_, wls, err := m.CreateProject(context.Background(), "p", "", []WorkloadSpec{
		{Preset: "expo"}, {Preset: "vite"}, {Preset: "api"}, {Preset: "tinbase"},
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, w := range wls {
		var want preset
		for _, p := range formerCatalog {
			if p.Image == w.Image && (p.Image != "" || w.Type == runtime.WorkloadTinbaseProject) {
				want = p
			}
		}
		if w.Type != want.Type || w.Image != want.Image {
			t.Errorf("workload %q: type %q image %q, want %q %q", w.Name, w.Type, w.Image, want.Type, want.Image)
		}
		wantMem := 384
		if w.Type == "rapidnative-dev" {
			wantMem = 3072
		}
		if w.MemoryMB != wantMem {
			t.Errorf("workload %q (%s): memory %d, want %d", w.Name, w.Type, w.MemoryMB, wantMem)
		}
	}
}

// Template workloads that aren't databases are labelled with the adopter's
// app type: RapidNative's clients find their phone workload by
// type == "rapidnative-dev", so that label must not change for them.
func TestTemplateAppWorkloadLabel(t *testing.T) {
	tmplDir := t.TempDir()
	mb, _ := json.Marshal(map[string]any{
		"name": "demo",
		"workloads": []map[string]any{
			{"name": "mobile", "kind": "node", "dir": "mobile", "run": []string{"node", "index.js"}},
			{"name": "db", "kind": "tinbase"},
		},
	})
	if err := os.WriteFile(filepath.Join(tmplDir, "orchd.json"), mb, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(tmplDir, "mobile"), 0o755); err != nil {
		t.Fatal(err)
	}
	for _, c := range []struct {
		rn   bool
		want runtime.WorkloadType
	}{{true, "rapidnative-dev"}, {false, runtime.WorkloadApp}} {
		m, _ := compatManager(t, c.rn)
		if err := m.SetTemplate("demo", tmplDir); err != nil {
			t.Fatal(err)
		}
		_, wls, err := m.CreateFromTemplate(context.Background(), "demo", "", "", nil, nil)
		if err != nil {
			t.Fatal(err)
		}
		for _, w := range wls {
			if w.Workspace == "mobile" && w.Type != c.want {
				t.Errorf("rapidnative=%v: mobile workload type %q, want %q", c.rn, w.Type, c.want)
			}
			if w.Workspace == "db" && w.Type != runtime.WorkloadTinbaseProject {
				t.Errorf("db workload type %q", w.Type)
			}
		}
	}
}

// Workloads stored with the old label keep their app behavior whatever the
// adopter file says: the engine classifies by "is it the database", not by label.
func TestStoredLegacyLabelIsStillAnApp(t *testing.T) {
	for _, rn := range []bool{true, false} {
		m, _ := compatManager(t, rn)
		if !legacyDevType.IsApp() {
			t.Fatal("rapidnative-dev must classify as an app workload")
		}
		if mem, cpus := m.defaultLimits(legacyDevType); mem != 3072 || cpus != 2 {
			t.Errorf("rapidnative=%v: legacy app limits = %d/%v", rn, mem, cpus)
		}
		if mem, _ := m.defaultLimits(runtime.WorkloadTinbaseProject); mem != 384 {
			t.Errorf("rapidnative=%v: tinbase limits = %d", rn, mem)
		}
	}
	if runtime.WorkloadType("").IsApp() || runtime.WorkloadTinbaseProject.IsApp() {
		t.Fatal("empty and database types must not be apps")
	}
}
