package runtime

import (
	"path/filepath"
	"reflect"
	"testing"
)

func TestShortDigest(t *testing.T) {
	cases := []struct{ in, want string }{
		{"sha256:9f86d081884c7d659a2feaa0c55ad015a3bf4f1b2b0b822cd15d6c15b0f00a08", "9f86d081884c"},
		{"sha256:abc", "sha256:abc"},     // too short to truncate: left as-is
		{"a1b2c3d4e5f6", "a1b2c3d4e5f6"}, // no scheme prefix: unchanged
		{"", ""},
	}
	for _, c := range cases {
		if got := shortDigest(c.in); got != c.want {
			t.Errorf("shortDigest(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestReadOnlyMountArgs(t *testing.T) {
	a, b := t.TempDir(), t.TempDir()
	missing := filepath.Join(a, "not-there")
	got := readOnlyMountArgs(map[string]string{b: "/shared-b", a: "/shared-a", missing: "/nope"})
	// Sorted by host dir; a host dir that doesn't exist is left out rather than
	// letting docker create an empty one in its place.
	want := []string{"-v", a + ":/shared-a:ro", "-v", b + ":/shared-b:ro"}
	if a > b {
		want = []string{"-v", b + ":/shared-b:ro", "-v", a + ":/shared-a:ro"}
	}
	if !reflect.DeepEqual(got, want) {
		t.Errorf("readOnlyMountArgs = %q, want %q", got, want)
	}
	if got := readOnlyMountArgs(nil); len(got) != 0 {
		t.Errorf("no mounts configured: got %q", got)
	}
}
