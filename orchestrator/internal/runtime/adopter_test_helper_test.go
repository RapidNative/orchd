package runtime

import (
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/adopter"
)

// rapidNativeAdopterFile is one adopter's real config, shipped in deploy/. Tests
// that check RapidNative's behavior load it, so they test what production runs.
const rapidNativeAdopterFile = "../../../deploy/adopters/rapidnative/adopter.json"

// legacyDevType is the label RapidNative's app workloads carry (and every app
// workload stored before the engine became product-neutral).
const legacyDevType WorkloadType = "rapidnative-dev"

func loadRapidNativeAdopter(t *testing.T) adopter.Config {
	t.Helper()
	c, err := adopter.Load(rapidNativeAdopterFile)
	if err != nil {
		t.Fatalf("load %s: %v", rapidNativeAdopterFile, err)
	}
	return c
}

// withRapidNativeAdopter installs RapidNative's recipes and probe headers for
// the test, restoring the neutral engine after.
func withRapidNativeAdopter(t *testing.T) adopter.Config {
	t.Helper()
	c := loadRapidNativeAdopter(t)
	SetProcessRecipes(c.ProcessRecipes)
	SetReadyProbeHeaders(c.ReadyProbeHeaders)
	t.Cleanup(func() {
		SetProcessRecipes(nil)
		SetReadyProbeHeaders(nil)
	})
	return c
}
