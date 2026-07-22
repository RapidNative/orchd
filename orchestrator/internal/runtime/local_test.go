package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalKind(t *testing.T) {
	cases := []struct {
		spec Spec
		want string
	}{
		{Spec{Type: WorkloadTinbaseProject}, "tinbase"},
		{Spec{Type: WorkloadRapidNativeDev, Image: "rn-vite:dev"}, "vite"},
		{Spec{Type: WorkloadRapidNativeDev, Image: "rn-api:dev"}, "api"},
		{Spec{Type: WorkloadRapidNativeDev, Image: "rn-expo:dev"}, "expo"},
		{Spec{Type: WorkloadRapidNativeDev, Image: "ghcr.io/acme/x:1"}, ""},
	}
	for _, c := range cases {
		if got := localKind(c.spec); got != c.want {
			t.Errorf("localKind(%+v) = %q, want %q", c.spec, got, c.want)
		}
	}
}

// An image with no local recipe must be refused (pointing at the docker driver),
// but a known RapidNative app is now handled by the process driver.
func TestLocalDriverRejectsUnknownImage(t *testing.T) {
	d := NewLocalDriver("/nonexistent/tinbase", "")
	_, err := d.Create(context.Background(), Spec{
		Ref:     "w1",
		Type:    WorkloadRapidNativeDev,
		Image:   "ghcr.io/acme/custom:1",
		DataDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "no recipe") {
		t.Fatalf("expected a 'no recipe' error for an unknown image, got: %v", err)
	}
}

func TestRecipeScaffold(t *testing.T) {
	for _, kind := range []string{"vite", "api", "expo"} {
		r, ok := recipeFor(kind)
		if !ok {
			t.Fatalf("no recipe for %q", kind)
		}
		dir := t.TempDir()
		if err := r.scaffold(dir); err != nil {
			t.Fatalf("scaffold %q: %v", kind, err)
		}
		if _, err := os.Stat(filepath.Join(dir, "package.json")); err != nil {
			t.Errorf("%q: package.json not scaffolded: %v", kind, err)
		}
		if len(r.install) == 0 {
			t.Errorf("%q: expected an install step", kind)
		}
		if argv, _ := r.run(8101); len(argv) == 0 {
			t.Errorf("%q: empty run argv", kind)
		}
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
