package manager

import (
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/adopter"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
)

// rapidNativeAdopterFile is one adopter's real config, shipped in deploy/. Tests
// of RapidNative's behavior load it, so they check what production runs.
const rapidNativeAdopterFile = "../../../deploy/adopters/rapidnative/adopter.json"

// legacyDevType is the label RapidNative's app workloads carry, and every app
// workload stored before the engine became product-neutral.
const legacyDevType runtime.WorkloadType = "rapidnative-dev"

func rapidNativeAdopter(t testing.TB) adopter.Config {
	t.Helper()
	c, err := adopter.Load(rapidNativeAdopterFile)
	if err != nil {
		t.Fatalf("load %s: %v", rapidNativeAdopterFile, err)
	}
	return c
}
