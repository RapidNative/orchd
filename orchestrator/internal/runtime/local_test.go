package runtime

import (
	"context"
	"strings"
	"testing"
)

// The process driver only runs tinbase; asking it for a RapidNative dev app must
// fail loudly (not silently boot a tinbase on that port).
func TestLocalDriverRejectsNonTinbase(t *testing.T) {
	d := NewLocalDriver("/nonexistent/tinbase", "")
	_, err := d.Create(context.Background(), Spec{
		Ref:   "w1",
		Type:  WorkloadRapidNativeDev,
		Image: "rn-vite:dev",
	})
	if err == nil {
		t.Fatal("expected an error for a rapidnative-dev workload on the local driver")
	}
	if !strings.Contains(err.Error(), "only tinbase") {
		t.Fatalf("unexpected error: %v", err)
	}
}

func TestRingWriterBoundedTail(t *testing.T) {
	w := &ringWriter{max: 10}
	w.Write([]byte("abcdefgh"))
	w.Write([]byte("ijklmn")) // total 14 > max 10
	got := w.String()
	if len(got) != 10 {
		t.Fatalf("ring not bounded: len=%d (%q)", len(got), got)
	}
	if got != "efghijklmn" {
		t.Fatalf("ring kept wrong tail: %q", got)
	}
}
