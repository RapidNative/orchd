package manager

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/config"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
)

// TestBuildImageFreezesTarballAndVersions builds images from a template twice and
// asserts auto-incrementing versions, an on-disk tarball, and a store record. The
// template has only a tinbase workload, so no docker build is attempted (keeps the
// test hermetic on machines with or without Docker).
func TestBuildImageFreezesTarballAndVersions(t *testing.T) {
	tmplDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmplDir, "orchd.json"),
		[]byte(`{"name":"demo","workloads":[{"name":"db","kind":"tinbase"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "seed.sql"), []byte("select 1;"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, err := store.Open("")
	if err != nil {
		t.Fatal(err)
	}
	m := New(config.Config{DataRoot: t.TempDir()}, st, reapStub{})
	if err := m.SetTemplate("demo", tmplDir); err != nil {
		t.Fatal(err)
	}

	im1, err := m.BuildImage(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if im1.Version != "v1" {
		t.Fatalf("first version = %q, want v1", im1.Version)
	}
	if _, err := os.Stat(im1.Tarball); err != nil {
		t.Fatalf("tarball not written: %v", err)
	}

	im2, err := m.BuildImage(context.Background(), "demo")
	if err != nil {
		t.Fatal(err)
	}
	if im2.Version != "v2" {
		t.Fatalf("second version = %q, want v2", im2.Version)
	}

	if got := len(m.BuiltImages()); got != 2 {
		t.Fatalf("BuiltImages len = %d, want 2", got)
	}
	if _, err := m.BuiltImage("demo", "v1"); err != nil {
		t.Fatalf("GetImage v1: %v", err)
	}
	if v, ok := m.latestImageVersion("demo"); !ok || v != "v2" {
		t.Fatalf("latestImageVersion = %q,%v want v2,true", v, ok)
	}
}

// TestCreateFromImageMaterializesTarballPlusDelta is the boot-from-image path:
// build an image, create a project from it with a delta, and assert the
// workload's working tree was restored from the frozen tarball with the delta
// overlaid.
func TestCreateFromImageMaterializesTarballPlusDelta(t *testing.T) {
	tmplDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmplDir, "orchd.json"),
		[]byte(`{"name":"demo","workloads":[{"name":"db","kind":"tinbase"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(tmplDir, "seed.sql"), []byte("select 1;"), 0o644); err != nil {
		t.Fatal(err)
	}

	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir()}, st, reapStub{})
	if err := m.SetTemplate("demo", tmplDir); err != nil {
		t.Fatal(err)
	}
	if _, err := m.BuildImage(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}

	delta := map[string]string{"extra.txt": "from delta"}
	_, wls, err := m.CreateFromImage(context.Background(), "demo", "v1", "proj", "", delta, nil)
	if err != nil {
		t.Fatal(err)
	}
	if len(wls) != 1 {
		t.Fatalf("workloads = %d, want 1", len(wls))
	}
	w := wls[0]
	if w.ImageVer != "v1" {
		t.Fatalf("workload ImageVer = %q, want v1", w.ImageVer)
	}
	// Frozen base file restored from the tarball.
	if b, err := os.ReadFile(filepath.Join(w.DataDir, "seed.sql")); err != nil || string(b) != "select 1;" {
		t.Fatalf("base file not materialized from tarball: %q err=%v", b, err)
	}
	// Delta overlaid on top.
	if b, err := os.ReadFile(filepath.Join(w.DataDir, "extra.txt")); err != nil || string(b) != "from delta" {
		t.Fatalf("delta not applied: %q err=%v", b, err)
	}
}

// TestImportImageBootsWithoutTemplateOrTarball simulates the target side of a
// registry publish: an image imported from another instance (docker-only, no
// tarball, no template folder registered here). Creating a project from it must
// succeed using the frozen workload shape, and the workload must carry the
// registry docker ref for the driver to run.
func TestImportImageBootsWithoutTemplateOrTarball(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir()}, st, reapStub{})

	// No template registered, no tarball on disk — only the import spec.
	spec := ImportSpec{
		Template: "rapidnative",
		Version:  "v2",
		Dockers:  map[string]string{"api": "ghcr.io/acme/orchd-rapidnative-api:v2"},
		Workloads: []store.ImageWorkload{
			{Name: "api", Kind: "node", Workspace: "api", Image: "rn-api:dev"},
		},
	}
	im, err := m.ImportImage(spec)
	if err != nil {
		t.Fatal(err)
	}
	if !im.Imported || im.Tarball != "" {
		t.Fatalf("imported image should be docker-only: imported=%v tarball=%q", im.Imported, im.Tarball)
	}

	_, wls, err := m.CreateFromImage(context.Background(), "rapidnative", "v2", "proj", "", nil, nil)
	if err != nil {
		t.Fatalf("CreateFromImage from imported image: %v", err)
	}
	if len(wls) != 1 {
		t.Fatalf("workloads = %d, want 1", len(wls))
	}
	w := wls[0]
	if w.ImageVer != "v2" || w.Workspace != "api" {
		t.Fatalf("workload not wired to image: ver=%q ws=%q", w.ImageVer, w.Workspace)
	}
	// The docker driver resolves the registry ref via specFor.
	spec2 := m.specFor(w)
	if spec2.Image != "ghcr.io/acme/orchd-rapidnative-api:v2" {
		t.Fatalf("specFor image = %q, want the registry ref", spec2.Image)
	}
	// No tree was materialized (docker-only), so no seed marker.
	if _, err := os.Stat(filepath.Join(w.DataDir, ".orchd-seeded")); err == nil {
		t.Fatal("docker-only image should not seed a working tree")
	}
}

// TestPushImageRequiresRegistry errors clearly when no registry is set.
func TestPushImageRequiresRegistry(t *testing.T) {
	tmplDir := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmplDir, "orchd.json"),
		[]byte(`{"name":"demo","workloads":[{"name":"db","kind":"tinbase"}]}`), 0o644); err != nil {
		t.Fatal(err)
	}
	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir()}, st, reapStub{})
	_ = m.SetTemplate("demo", tmplDir)
	if _, err := m.BuildImage(context.Background(), "demo"); err != nil {
		t.Fatal(err)
	}
	// tinbase-only image → no docker tags, and no registry set: push must error.
	if _, err := m.PushImage(context.Background(), "demo", "v1"); err == nil {
		t.Fatal("expected push to fail without a registry")
	}
}

// TestBuildImageUnknownTemplate errors cleanly.
func TestBuildImageUnknownTemplate(t *testing.T) {
	st, _ := store.Open("")
	m := New(config.Config{DataRoot: t.TempDir()}, st, reapStub{})
	if _, err := m.BuildImage(context.Background(), "nope"); err == nil {
		t.Fatal("expected error for unknown template")
	}
}
