package backup

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestIDsRoundTripWithKind(t *testing.T) {
	ts := time.Date(2026, 10, 27, 8, 0, 0, 0, time.UTC)
	for _, kind := range []string{KindDir, KindDump} {
		id := backupID("ab12cd34ef", ts, kind)
		wid, got, k, err := parseID(id)
		if err != nil || wid != "ab12cd34ef" || !got.Equal(ts) || k != kind {
			t.Fatalf("%q: %v %v %q %v", id, wid, got, k, err)
		}
		name := fileName(ts, kind)
		got, k, ok := parseFileName(name)
		if !ok || !got.Equal(ts) || k != kind {
			t.Fatalf("%q: %v %q %v", name, got, k, ok)
		}
	}
	// ids from before kinds existed are data-dir archives
	if _, _, k, err := parseID("ab12cd34ef__20261027T080000Z"); err != nil || k != KindDir {
		t.Fatalf("legacy id: %q %v", k, err)
	}
	for _, bad := range []string{"ab12cd34ef__20261027T080000Z.zip", "nounderscores"} {
		if _, _, _, err := parseID(bad); err == nil {
			t.Fatalf("%q parsed", bad)
		}
	}
	if _, _, ok := parseFileName("20261027T080000Z.partial"); ok {
		t.Fatal("partial file listed")
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestLocalPutFetchAndRestoreRefusal(t *testing.T) {
	s, err := NewLocalStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	src := filepath.Join(t.TempDir(), "export.tar.gz")
	writeFile(t, src, "dump-bytes")

	b, err := s.Put("w1", src, KindDump)
	if err != nil {
		t.Fatal(err)
	}
	if b.Kind != KindDump || b.SizeBytes != int64(len("dump-bytes")) {
		t.Fatalf("put: %+v", b)
	}
	list, err := s.List("w1")
	if err != nil || len(list) != 1 || list[0].ID != b.ID || list[0].Kind != KindDump {
		t.Fatalf("list: %+v %v", list, err)
	}

	dst := filepath.Join(t.TempDir(), "got.tar.gz")
	if err := s.Fetch(b.ID, dst); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(dst); string(got) != "dump-bytes" {
		t.Fatalf("fetch: %q", got)
	}
	if err := s.Restore(b.ID, t.TempDir()); !errors.Is(err, ErrNotExtractable) {
		t.Fatalf("restore of a dump: %v", err)
	}
}

func TestMultiStoreReadsOlderStores(t *testing.T) {
	oldStore, _ := NewLocalStore(t.TempDir())
	newStore, _ := NewLocalStore(t.TempDir())
	data := t.TempDir()
	writeFile(t, filepath.Join(data, "db", "PG_VERSION"), "17")

	// an archive taken before the switch, then one after
	legacy, err := oldStore.Create("w1", data, nil)
	if err != nil {
		t.Fatal(err)
	}
	time.Sleep(1100 * time.Millisecond) // ids have second resolution
	m := NewMultiStore(newStore, oldStore)
	src := filepath.Join(t.TempDir(), "export.tar.gz")
	writeFile(t, src, "dump")
	fresh, err := m.Put("w1", src, KindDump)
	if err != nil {
		t.Fatal(err)
	}
	if l, _ := newStore.List("w1"); len(l) != 1 {
		t.Fatalf("put did not go to the primary: %+v", l)
	}

	list, err := m.List("w1")
	if err != nil || len(list) != 2 || list[0].ID != fresh.ID || list[1].ID != legacy.ID {
		t.Fatalf("list: %+v %v", list, err)
	}

	// the legacy archive still restores, from the store that has it
	dest := t.TempDir()
	if err := m.Restore(legacy.ID, dest); err != nil {
		t.Fatal(err)
	}
	if got, _ := os.ReadFile(filepath.Join(dest, "db", "PG_VERSION")); string(got) != "17" {
		t.Fatalf("restored %q", got)
	}

	// retention spans both stores
	if err := m.Retain("w1", 1); err != nil {
		t.Fatal(err)
	}
	if l, _ := oldStore.List("w1"); len(l) != 0 {
		t.Fatalf("legacy archive kept past retention: %+v", l)
	}
	if l, _ := m.List("w1"); len(l) != 1 || l[0].ID != fresh.ID {
		t.Fatalf("after retain: %+v", l)
	}
	if err := m.Delete("w1__20200101T000000Z"); err != nil {
		t.Fatalf("deleting a missing backup: %v", err)
	}
}
