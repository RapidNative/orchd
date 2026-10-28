package manager

import (
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// A workload stores its memory cap when it is created; ORCHD_IMAGE_MIN_MEMORY_MB
// raises it for every workload on that image, and never lowers or caps one.
func TestSpecForImageMinMemory(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir(), ImageMinMemoryMB: map[string]int{"rn-run:dev": 2048}}, st, reapStub{})
	for _, c := range []struct {
		image     string
		mem, want int
	}{
		{"rn-run:dev", 1024, 2048}, // raised
		{"rn-run:dev", 4096, 4096}, // already above
		{"rn-run:dev", 0, 0},       // unlimited stays unlimited
		{"rn-web:dev", 1024, 1024}, // other images untouched
	} {
		got := m.specFor(&store.Workload{ID: "w1", Image: c.image, MemoryMB: c.mem}).Limits.MemoryMB
		if got != c.want {
			t.Errorf("%s at %dMB: spec cap = %d, want %d", c.image, c.mem, got, c.want)
		}
	}
}
