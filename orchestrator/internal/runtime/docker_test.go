package runtime

import "testing"

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
