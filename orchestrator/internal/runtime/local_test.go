package runtime

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLocalKind(t *testing.T) {
	// The neutral engine runs tinbase locally and no app image.
	if got := localKind(Spec{Type: WorkloadApp, Image: "rn-vite:dev"}); got != "" {
		t.Errorf("no adopter: localKind(rn-vite:dev) = %q, want none", got)
	}
	withRapidNativeAdopter(t)
	cases := []struct {
		spec Spec
		want string
	}{
		{Spec{Type: WorkloadTinbaseProject}, "tinbase"},
		{Spec{Type: legacyDevType, Image: "rn-vite:dev"}, "rn-vite:dev"},
		{Spec{Type: legacyDevType, Image: "rn-api:dev"}, "rn-api:dev"},
		{Spec{Type: legacyDevType, Image: "rn-expo:dev"}, "rn-expo:dev"},
		{Spec{Type: legacyDevType, Image: "ghcr.io/acme/x:1"}, ""},
	}
	for _, c := range cases {
		if got := localKind(c.spec); got != c.want {
			t.Errorf("localKind(%+v) = %q, want %q", c.spec, got, c.want)
		}
	}
}

// An image with no local recipe must be refused (pointing at the docker driver),
// but an image the adopter gave a recipe is handled by the process driver.
func TestLocalDriverRejectsUnknownImage(t *testing.T) {
	withRapidNativeAdopter(t)
	d := NewLocalDriver("/nonexistent/tinbase", "")
	_, err := d.Create(context.Background(), Spec{
		Ref:     "w1",
		Type:    legacyDevType,
		Image:   "ghcr.io/acme/custom:1",
		DataDir: t.TempDir(),
	})
	if err == nil || !strings.Contains(err.Error(), "no recipe") {
		t.Fatalf("expected a 'no recipe' error for an unknown image, got: %v", err)
	}
}

func TestRecipeScaffold(t *testing.T) {
	withRapidNativeAdopter(t)
	for _, kind := range []string{"rn-vite:dev", "rn-api:dev", "rn-expo:dev"} {
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

// The adopter's recipes reproduce the run commands the engine used to hard-code
// for RapidNative's images, byte for byte.
func TestRapidNativeRecipesMatchFormerBuiltins(t *testing.T) {
	withRapidNativeAdopter(t)
	want := map[string]struct{ argv, env []string }{
		"rn-vite:dev": {[]string{"npm", "run", "dev", "--", "--host", "127.0.0.1", "--port", "8101", "--strictPort"}, nil},
		"rn-api:dev":  {[]string{"node", "server.mjs"}, []string{"PORT=8101"}},
		"rn-expo:dev": {[]string{"npx", "expo", "start", "--web", "--port", "8101"}, []string{"CI=1", "BROWSER=none"}},
	}
	for image, w := range want {
		r, ok := recipeFor(localKind(Spec{Type: legacyDevType, Image: image}))
		if !ok {
			t.Fatalf("%s: no recipe", image)
		}
		argv, env := r.run(8101)
		if strings.Join(argv, " ") != strings.Join(w.argv, " ") || strings.Join(env, " ") != strings.Join(w.env, " ") {
			t.Errorf("%s: run = %q %q, want %q %q", image, argv, env, w.argv, w.env)
		}
		if strings.Join(r.install, " ") != "npm install --no-audit --no-fund" {
			t.Errorf("%s: install = %q", image, r.install)
		}
	}
}
