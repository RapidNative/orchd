package template

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestBundleThenMaterializeFromTar is the boot-from-image round-trip: freeze a
// template tree to a tarball, then restore it into a workload dir with a delta
// overlaid and a tombstone applied.
func TestBundleThenMaterializeFromTar(t *testing.T) {
	base := t.TempDir()
	mustWrite(t, filepath.Join(base, "orchd.json"), `{"name":"x"}`)
	mustWrite(t, filepath.Join(base, "api", "index.js"), "console.log('base')")
	mustWrite(t, filepath.Join(base, "README.md"), "keep me")
	// node_modules must be excluded from the frozen tarball.
	mustWrite(t, filepath.Join(base, "node_modules", "junk.js"), "nope")

	tarPath := filepath.Join(t.TempDir(), "base.tar.gz")
	f, err := os.Create(tarPath)
	if err != nil {
		t.Fatal(err)
	}
	if err := Bundle(base, nil, f); err != nil {
		t.Fatal(err)
	}
	f.Close()

	dest := t.TempDir()
	delta := map[string]string{"api/index.js": "console.log('delta')"}
	deleted := []string{"README.md"}
	if err := MaterializeFromTar(tarPath, dest, delta, deleted); err != nil {
		t.Fatal(err)
	}

	// Delta wins over the base file.
	if got := readFile(t, filepath.Join(dest, "api", "index.js")); got != "console.log('delta')" {
		t.Fatalf("delta not applied: %q", got)
	}
	// Tombstoned file is gone.
	if _, err := os.Stat(filepath.Join(dest, "README.md")); !os.IsNotExist(err) {
		t.Fatalf("tombstone not applied: %v", err)
	}
	// Base file with no delta survives.
	if got := readFile(t, filepath.Join(dest, "orchd.json")); got != `{"name":"x"}` {
		t.Fatalf("base file lost: %q", got)
	}
	// node_modules never entered the tarball.
	if _, err := os.Stat(filepath.Join(dest, "node_modules")); !os.IsNotExist(err) {
		t.Fatal("node_modules leaked into the image")
	}
}

// TestUntarRejectsTraversal ensures a hostile tar entry can't escape destDir.
// safeJoin neutralizes "../" by cleaning the path against "/", so the entry lands
// inside destDir rather than escaping it.
func TestUntarRejectsTraversal(t *testing.T) {
	root := t.TempDir()
	got := safeJoin(root, "../../escape")
	if got == "" {
		return // rejected outright is also acceptable
	}
	if !strings.HasPrefix(got, root+string(os.PathSeparator)) && got != root {
		t.Fatalf("safeJoin escaped root: %q not under %q", got, root)
	}
}

func mustWrite(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func readFile(t *testing.T, path string) string {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	return string(bytes.TrimRight(b, "\n"))
}
