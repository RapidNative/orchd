package runtime

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

// fakeDocker puts a `docker` script first on PATH that answers the inspect
// queries Start makes and records every invocation, so the wake decision can
// be tested without a daemon. The container exists and is stopped, publishes
// port, was created from containerImage, and the target tag resolves to
// targetImage ("" = the image inspect fails).
func fakeDocker(t *testing.T, port, containerImage, targetImage string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	gone := filepath.Join(dir, "removed")
	script := `#!/bin/sh
echo "$*" >> "` + logPath + `"
case "$*" in
  "rm -f "*) touch "` + gone + `" ;;
  "run -d "*) rm -f "` + gone + `" ;;
  "inspect tb-"*) [ -e "` + gone + `" ] && exit 1 ;;
  "inspect -f {{.State.Running}} "*) [ -e "` + gone + `" ] && exit 1; echo false ;;
  "inspect -f {{range"*) echo "` + port + `" ;;
  "inspect -f {{.Image}} "*) echo "` + containerImage + `" ;;
  "image inspect "*) [ -n "` + targetImage + `" ] && echo "` + targetImage + `" || exit 1 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))
	return logPath
}

// readyServer answers any request, standing in for the woken workload.
func readyServer(t *testing.T) (port string) {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {}))
	t.Cleanup(srv.Close)
	_, port, _ = net.SplitHostPort(srv.Listener.Addr().String())
	return port
}

func dockerCalls(t *testing.T, logPath string) []string {
	t.Helper()
	b, _ := os.ReadFile(logPath)
	return strings.Split(strings.TrimSpace(string(b)), "\n")
}

func ranCmd(cs []string, prefix string) bool {
	for _, c := range cs {
		if strings.HasPrefix(c, prefix) {
			return true
		}
	}
	return false
}

func wakeW1(t *testing.T, port string) {
	t.Helper()
	d := NewDockerDriver("db-image:new", "")
	p, _ := strconv.Atoi(port)
	spec := Spec{Ref: "w1", DataDir: t.TempDir(), HostPort: p}
	if _, err := d.Start(context.Background(), spec); err != nil {
		t.Fatalf("Start: %v", err)
	}
}

func TestStartRecreatesWhenImageChanged(t *testing.T) {
	port := readyServer(t)
	logPath := fakeDocker(t, port, "sha256:old", "sha256:new")
	wakeW1(t, port)
	cs := dockerCalls(t, logPath)
	if !ranCmd(cs, "run -d --name tb-w1") || ranCmd(cs, "start tb-w1") {
		t.Fatalf("want a recreate (docker run), not docker start; calls:\n%s", strings.Join(cs, "\n"))
	}
	if !ranCmd(cs, "rm -f tb-w1") {
		t.Fatalf("want the old container removed before the recreate; calls:\n%s", strings.Join(cs, "\n"))
	}
	// The recreate must remount the same data dir, so nothing stored is lost.
	if !ranCmd(cs, "run -d --name tb-w1 --restart no -p 127.0.0.1:"+port+":54321 -v ") {
		t.Fatalf("want /data remounted on recreate; calls:\n%s", strings.Join(cs, "\n"))
	}
}

func TestStartResumesWhenImageUnchanged(t *testing.T) {
	port := readyServer(t)
	logPath := fakeDocker(t, port, "sha256:same", "sha256:same")
	wakeW1(t, port)
	cs := dockerCalls(t, logPath)
	if !ranCmd(cs, "start tb-w1") || ranCmd(cs, "run -d") {
		t.Fatalf("want docker start, not a recreate; calls:\n%s", strings.Join(cs, "\n"))
	}
}

func TestStartResumesWhenTargetImageUnreadable(t *testing.T) {
	port := readyServer(t)
	logPath := fakeDocker(t, port, "sha256:old", "")
	wakeW1(t, port)
	cs := dockerCalls(t, logPath)
	if !ranCmd(cs, "start tb-w1") || ranCmd(cs, "run -d") {
		t.Fatalf("an unreadable target image must never block a wake; calls:\n%s", strings.Join(cs, "\n"))
	}
}
