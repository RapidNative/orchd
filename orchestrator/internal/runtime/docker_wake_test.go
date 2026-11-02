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
	return fakeDockerStartFailing(t, port, containerImage, targetImage, "")
}

// fakeDockerStartFailing is fakeDocker whose `docker start` fails with startErr
// (a non-empty daemon message), the way a stuck suspended container does. A
// fake `ctr` next to it records its calls in the same log.
func fakeDockerStartFailing(t *testing.T, port, containerImage, targetImage, startErr string) (logPath string) {
	t.Helper()
	dir := t.TempDir()
	logPath = filepath.Join(dir, "calls.log")
	gone := filepath.Join(dir, "removed")
	script := `#!/bin/sh
echo "$*" >> "` + logPath + `"
case "$*" in
  "rm -f "*) touch "` + gone + `" ;;
  "run -d "*) rm -f "` + gone + `" ;;
  "start "*) [ -n "` + startErr + `" ] && { echo "Error response from daemon: ` + startErr + `"; exit 1; } ;;
  "inspect tb-"*) [ -e "` + gone + `" ] && exit 1 ;;
  "inspect -f {{.State.Running}} "*) [ -e "` + gone + `" ] && exit 1; echo false ;;
  "inspect -f {{range"*) echo "` + port + `" ;;
  "inspect -f {{.Image}} "*) echo "` + containerImage + `" ;;
  "inspect -f {{.Id}} "*) echo "5a5cab794f51a51f522c048b71db3b82621ba6811b67999ae0fccfe567a5c162" ;;
  "image inspect "*) [ -n "` + targetImage + `" ] && echo "` + targetImage + `" || exit 1 ;;
esac
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "docker"), []byte(script), 0o755); err != nil {
		t.Fatal(err)
	}
	ctr := `#!/bin/sh
echo "ctr $*" >> "` + logPath + `"
exit 0
`
	if err := os.WriteFile(filepath.Join(dir, "ctr"), []byte(ctr), 0o755); err != nil {
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

// A suspended container whose host port was handed to another container since
// it was created: `docker start` fails for good ("port is already allocated"),
// so the wake must recreate (rm -f + run on the same /data, fresh port) instead
// of failing every wake until someone recreates by hand (FloorSync, 2026-11-02).
func TestStartRecreatesWhenStartFailsOnPortInUse(t *testing.T) {
	port := readyServer(t)
	logPath := fakeDockerStartFailing(t, port, "sha256:same", "sha256:same",
		"failed to set up container networking: driver failed programming external connectivity on endpoint tb-w1 (79944bf4): Bind for 127.0.0.1:36315 failed: port is already allocated")
	wakeW1(t, port)
	cs := dockerCalls(t, logPath)
	if !ranCmd(cs, "start tb-w1") {
		t.Fatalf("want docker start tried first; calls:\n%s", strings.Join(cs, "\n"))
	}
	if !ranCmd(cs, "rm -f tb-w1") || !ranCmd(cs, "run -d --name tb-w1 --restart no -p 127.0.0.1:"+port+":54321 -v ") {
		t.Fatalf("want a recreate on the same /data after the failed start; calls:\n%s", strings.Join(cs, "\n"))
	}
	if ranCmd(cs, "ctr ") {
		t.Fatalf("a port collision leaves no stale task; ctr must not run; calls:\n%s", strings.Join(cs, "\n"))
	}
}

// After one failed start docker reports a containerd task it cannot delete
// ("cannot signal container ... in state created") on every later start, and
// `docker rm -f` hits the same wall. The wake removes that task through ctr
// before recreating (Marriage & Match: 19k failed wakes over 3.5 days).
func TestStartClearsStaleTaskThenRecreates(t *testing.T) {
	port := readyServer(t)
	logPath := fakeDockerStartFailing(t, port, "sha256:same", "sha256:same",
		"could not delete stale containerd task object: unknown error after kill: /usr/bin/runsc did not terminate successfully: exit status 128: cannot signal container \"5a5cab79\" in state created")
	wakeW1(t, port)
	cs := dockerCalls(t, logPath)
	if !ranCmd(cs, "ctr -n moby task delete --force 5a5cab794f51a51f522c048b71db3b82621ba6811b67999ae0fccfe567a5c162") {
		t.Fatalf("want the stale containerd task deleted through ctr; calls:\n%s", strings.Join(cs, "\n"))
	}
	if !ranCmd(cs, "rm -f tb-w1") || !ranCmd(cs, "run -d --name tb-w1") {
		t.Fatalf("want a recreate after clearing the task; calls:\n%s", strings.Join(cs, "\n"))
	}
	ctrAt, rmAt := -1, -1
	for i, c := range cs {
		if strings.HasPrefix(c, "ctr ") && ctrAt < 0 {
			ctrAt = i
		}
		if strings.HasPrefix(c, "rm -f tb-w1") && rmAt < 0 {
			rmAt = i
		}
	}
	if ctrAt > rmAt {
		t.Fatalf("the task must be cleared before rm -f; calls:\n%s", strings.Join(cs, "\n"))
	}
}
