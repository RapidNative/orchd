//go:build linux

package runtime

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The manager hands the per-version deps path, which is a symlink into the
// content-addressed shared-deps store. The family name MUST come from the
// resolved target: naming it after the link's basename ("mobile") collapses
// every lockfile into one family, and templates would serve each other's
// dependency trees.
func TestDepsNameResolvesSymlinkToContentAddress(t *testing.T) {
	root := t.TempDir()
	shared := filepath.Join(root, "shared-deps", "ab12cd34ef56-mobile")
	if err := os.MkdirAll(shared, 0o755); err != nil {
		t.Fatal(err)
	}
	verDeps := filepath.Join(root, "v47", "deps")
	if err := os.MkdirAll(verDeps, 0o755); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(verDeps, "mobile")
	if err := os.Symlink(shared, link); err != nil {
		t.Fatal(err)
	}

	if got := depsName(link); got != "ab12cd34ef56-mobile" {
		t.Fatalf("depsName(%q) = %q, want the content-addressed target name", link, got)
	}
	// A plain dir (no symlink) keeps its own name.
	if got := depsName(shared); got != "ab12cd34ef56-mobile" {
		t.Fatalf("plain dir: %q", got)
	}
}

// The init template must carry the optional overlay block, guarded on the
// device actually existing, so images without a deps drive boot unchanged.
func TestInitTemplateDepsOverlayIsGuarded(t *testing.T) {
	if !strings.Contains(fcInitTemplate, "if [ -b /dev/vdb ]; then") {
		t.Fatal("deps overlay must be guarded on /dev/vdb existing")
	}
	if !strings.Contains(fcInitTemplate, "lowerdir=/deps/node_modules,upperdir=/data/.deps-upper") {
		t.Fatal("overlay lower/upper wiring missing from init")
	}
	if strings.Index(fcInitTemplate, "/dev/vdb") > strings.Index(fcInitTemplate, "orchd.env") {
		t.Fatal("overlay must mount before the workload env/exec")
	}
}
