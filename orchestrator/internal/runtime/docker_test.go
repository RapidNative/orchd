package runtime

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"strings"
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

func TestRunOfflineIsolatesTheExport(t *testing.T) {
	bin := t.TempDir()
	log := filepath.Join(bin, "calls")
	script := "#!/bin/sh\necho \"$@\" >> " + log + "\n[ \"$1\" = inspect ] && exit 1\nexit 0\n"
	if err := os.WriteFile(filepath.Join(bin, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))

	d := NewDockerDriver("tinbase:0.22.0", "runsc")
	if _, err := d.RunOffline(context.Background(), "w1", "", "/data/p/w1", "tinbase", "db", "export"); err != nil {
		t.Fatal(err)
	}
	b, _ := os.ReadFile(log)
	var run string
	for _, l := range strings.Split(strings.TrimSpace(string(b)), "\n") {
		if strings.HasPrefix(l, "run ") {
			run = l
		}
	}
	want := "run --rm --name tb-w1-offline --network none --memory 512m -v /data/p/w1:/data --runtime runsc tinbase:0.22.0 tinbase db export"
	if run != want {
		t.Fatalf("docker %s\nwant   %s", run, want)
	}
}
