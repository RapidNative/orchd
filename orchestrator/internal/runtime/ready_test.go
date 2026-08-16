package runtime

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

// The readiness probe must identify itself as a native Expo client. A bare
// GET / makes an Expo dev server SSR the web target — bundling the whole app
// in guest memory as the first act of a cold boot, which OOM-killed every
// workspace whose deps diverged from the baked image (boot → build → die →
// repeat, forever). With the header Expo answers its native manifest in
// milliseconds; non-Expo servers ignore it.
func TestReadinessProbeSpeaksExpo(t *testing.T) {
	got := make(chan *http.Request, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		got <- r.Clone(context.Background())
	}))
	defer srv.Close()

	if err := waitHTTP(context.Background(), strings.TrimPrefix(srv.URL, "http://"), 5*time.Second); err != nil {
		t.Fatalf("waitHTTP: %v", err)
	}
	r := <-got
	if r.Header.Get("expo-platform") != "ios" {
		t.Fatalf("probe sent expo-platform=%q, want ios (a bare GET / triggers web SSR and OOM)", r.Header.Get("expo-platform"))
	}
	if !strings.Contains(r.Header.Get("Accept"), "application/expo+json") {
		t.Fatalf("probe Accept=%q, want application/expo+json", r.Header.Get("Accept"))
	}
}
