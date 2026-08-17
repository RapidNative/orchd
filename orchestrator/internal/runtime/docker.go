package runtime

import (
	"bytes"
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

// DockerDriver runs each workload as a Docker container, optionally under the
// gVisor (runsc) runtime for VM-grade isolation without KVM.
//
// Suspend/Start map to `docker stop`/`docker start`. A workload may be placed on
// a specific Docker daemon (a region's worker node) via Spec.DockerHost; the
// driver remembers each ref's host so later ops (stop/status/logs) target the
// right daemon, publishes remotely-placed containers on 0.0.0.0, and addresses
// them by the node host so the gateway can reach them across nodes.
type DockerDriver struct {
	Image         string // container image (e.g. "tinbase:0.10.0")
	Runtime       string // "runsc" = gVisor; empty = runc
	ContainerPort int    // port the workload listens on inside the container
	DockerHost    string // default daemon; empty = local

	mu    sync.Mutex
	hosts map[string]string // ref -> DockerHost it was placed on
}

func NewDockerDriver(image, dockerRuntime string) *DockerDriver {
	return &DockerDriver{
		Image:         image,
		Runtime:       dockerRuntime,
		ContainerPort: 54321,
		hosts:         make(map[string]string),
	}
}

func (d *DockerDriver) Name() string {
	if d.Runtime != "" {
		return "docker+" + d.Runtime
	}
	return "docker"
}

func containerName(ref string) string { return "tb-" + ref }

// depsVolumeName is the per-workload named volume that preserves the image's
// installed dependencies (node_modules) across the source mount. It survives
// suspend/wake (fast boots) and is removed with the workload.
func depsVolumeName(ref string) string { return "tb-" + ref + "-deps" }

// RemoveVolumes deletes the workload's named volumes and unmounts its deps
// overlay (called on workload deletion; suspend keeps both so wakes stay
// fast). Best-effort. The overlay must be unmounted before the manager
// reclaims the data dir, or the removal would recurse into the mount.
func (d *DockerDriver) RemoveVolumes(ctx context.Context, ref string) error {
	return d.docker(ctx, d.hostFor(ref), "volume", "rm", "-f", depsVolumeName(ref)).Run()
}

// RemoveWorkloadMounts unmounts the per-workload deps overlay under dataDir.
func RemoveWorkloadMounts(dataDir string) {
	merged := filepath.Join(dataDir, ".deps", "merged")
	if exec.Command("mountpoint", "-q", merged).Run() == nil {
		_ = exec.Command("umount", "-l", merged).Run()
	}
}

// prepareDepsOverlay mounts (idempotently) a writable overlay whose lower
// layer is the shared per-image deps extraction and whose upper layer is
// per-workload scratch under the data dir. Linux-only; callers fall back to a
// named volume when it fails (macOS dev, missing privileges).
func prepareDepsOverlay(dataDir, depsHost string) (string, error) {
	base := filepath.Join(dataDir, ".deps")
	merged := filepath.Join(base, "merged")
	upper := filepath.Join(base, "upper")
	work := filepath.Join(base, "work")
	for _, d := range []string{merged, upper, work} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return "", err
		}
	}
	if exec.Command("mountpoint", "-q", merged).Run() == nil {
		return merged, nil // already mounted (wake after suspend / orchd restart)
	}
	opts := "lowerdir=" + depsHost + ",upperdir=" + upper + ",workdir=" + work
	if out, err := exec.Command("mount", "-t", "overlay", "overlay", "-o", opts, merged).CombinedOutput(); err != nil {
		return "", fmt.Errorf("overlay mount: %v: %s", err, strings.TrimSpace(string(out)))
	}
	return merged, nil
}

// WriteFileInContainer writes content to a path inside a running container
// via docker exec. The bind mount makes the bytes identical to a host-side
// write — the point is the inotify event: watchers inside the container
// (Metro, node --watch) do not see events for host-side writes under gVisor,
// so a file written from outside is invisible to them until a restart.
// Returns an error when the container isn't running; callers fall back to the
// host-side write (a later boot crawls the tree fresh anyway).
func (d *DockerDriver) WriteFileInContainer(ctx context.Context, ref, containerPath string, content []byte) error {
	quoted := "'" + strings.ReplaceAll(containerPath, "'", `'\''`) + "'"
	cmd := d.docker(ctx, d.hostFor(ref), "exec", "-i", containerName(ref),
		"sh", "-c", "mkdir -p \"$(dirname "+quoted+")\" && cat > "+quoted)
	cmd.Stdin = bytes.NewReader(content)
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("exec write %s: %v: %s", containerPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// DeleteFileInContainer removes a path inside a running container (see
// WriteFileInContainer for why deletion also goes through the namespace).
func (d *DockerDriver) DeleteFileInContainer(ctx context.Context, ref, containerPath string) error {
	quoted := "'" + strings.ReplaceAll(containerPath, "'", `'\''`) + "'"
	if out, err := d.docker(ctx, d.hostFor(ref), "exec", containerName(ref),
		"sh", "-c", "rm -rf "+quoted).CombinedOutput(); err != nil {
		return fmt.Errorf("exec delete %s: %v: %s", containerPath, err, strings.TrimSpace(string(out)))
	}
	return nil
}

// removeAndWait force-removes a container and blocks until its name is
// actually free. Removal of a running container (especially under gVisor, or
// a loaded daemon) completes asynchronously — a docker run issued right after
// `rm -f` races it and fails with "Conflict: name already in use".
func (d *DockerDriver) removeAndWait(ctx context.Context, host, name string) {
	_ = d.docker(ctx, host, "rm", "-f", name).Run()
	for i := 0; i < 60; i++ {
		if err := d.docker(ctx, host, "inspect", name).Run(); err != nil {
			return // name is free
		}
		_ = d.docker(ctx, host, "rm", "-f", name).Run()
		time.Sleep(500 * time.Millisecond)
	}
}

func (d *DockerDriver) setHost(ref, host string) {
	d.mu.Lock()
	d.hosts[ref] = host
	d.mu.Unlock()
}

func (d *DockerDriver) hostFor(ref string) string {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.hosts[ref]
}

// effHost resolves the daemon for an op: the per-workload host, else the default.
func (d *DockerDriver) effHost(host string) string {
	if host != "" {
		return host
	}
	return d.DockerHost
}

func isRemote(h string) bool { return h != "" && !strings.HasPrefix(h, "unix://") }

// nodeHost extracts the reachable hostname from a Docker host URL
// (tcp://host:2375, ssh://root@host) — the address the gateway proxies to.
func nodeHost(h string) string {
	s := h
	for _, p := range []string{"tcp://", "ssh://", "http://", "https://"} {
		s = strings.TrimPrefix(s, p)
	}
	if i := strings.LastIndex(s, "@"); i >= 0 {
		s = s[i+1:]
	}
	if i := strings.IndexByte(s, ':'); i >= 0 {
		s = s[:i]
	}
	if i := strings.IndexByte(s, '/'); i >= 0 {
		s = s[:i]
	}
	if s == "" {
		return "127.0.0.1"
	}
	return s
}

// placement returns the port-publish bind address and the address host to use in
// Instance.Addr for a given daemon.
func placement(eff string) (bind, addrHost string) {
	if isRemote(eff) {
		return "0.0.0.0", nodeHost(eff)
	}
	return "127.0.0.1", "127.0.0.1"
}

func (d *DockerDriver) Create(ctx context.Context, spec Spec) (*Instance, error) {
	host := spec.DockerHost
	d.setHost(spec.Ref, host)
	bind, addrHost := placement(d.effHost(host))

	d.removeAndWait(ctx, host, containerName(spec.Ref))

	if err := os.MkdirAll(spec.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	// A pinned HostPort gives stable port-per-workload addressing; otherwise pick
	// a free one (gateway/subdomain model).
	hostPort := spec.HostPort
	if hostPort == 0 {
		var err error
		if hostPort, err = freePort(); err != nil {
			return nil, fmt.Errorf("allocate port: %w", err)
		}
	}

	image := d.Image
	if spec.Image != "" {
		image = spec.Image
	}
	cport := d.ContainerPort
	if spec.Port != 0 {
		cport = spec.Port
	}

	args := []string{
		"run", "-d",
		"--name", containerName(spec.Ref),
		"--restart", "no",
		"-p", fmt.Sprintf("%s:%d:%d", bind, hostPort, cport),
		"-v", spec.DataDir + ":/data",
	}
	// Volume-run: mount the seeded workspace source over the image's app dir so
	// the container runs the per-project tree (deltas + live file sync), not
	// the code baked at build. DepsPath (node_modules) is shadowed by a named
	// per-workload volume that docker initializes from the image's content.
	if spec.AppMount != "" {
		src := spec.DataDir
		if spec.WorkspaceDir != "" {
			src = filepath.Join(spec.DataDir, spec.WorkspaceDir)
		}
		args = append(args, "-v", src+":"+spec.AppMount)
		if spec.DepsPath != "" {
			mounted := false
			if spec.DepsHostDir != "" {
				// Shared deps extracted once per image version, with a small
				// per-workload writable overlay on top: node tooling writes
				// caches inside node_modules (react-native-css-interop writes
				// into its own package dir), so a plain :ro mount breaks it.
				if merged, err := prepareDepsOverlay(spec.DataDir, spec.DepsHostDir); err == nil {
					args = append(args, "-v", merged+":"+spec.DepsPath)
					mounted = true
				} else {
					fmt.Fprintf(os.Stderr, "deps overlay for %s unavailable (%v) — falling back to named volume\n", spec.Ref, err)
				}
			}
			if !mounted {
				args = append(args, "-v", depsVolumeName(spec.Ref)+":"+spec.DepsPath)
			}
		}
	}
	if d.Runtime != "" {
		args = append(args, "--runtime", d.Runtime)
	}
	if m := spec.Limits.MemoryMB; m > 0 {
		args = append(args, "--memory", fmt.Sprintf("%dm", m), "--memory-swap", fmt.Sprintf("%dm", m))
	}
	if c := spec.Limits.CPUs; c > 0 {
		args = append(args, "--cpus", strconv.FormatFloat(c, 'f', -1, 64))
	}
	if p := spec.Limits.PidsLimit; p > 0 {
		args = append(args, "--pids-limit", strconv.Itoa(p))
	}
	for k, v := range spec.Env {
		args = append(args, "-e", k+"="+v)
	}
	// Host aliases: steer well-known hostnames to on-box services (a cache, a
	// mirror) for this container only — public DNS stays untouched.
	for h, ip := range spec.HostAliases {
		args = append(args, "--add-host", h+":"+ip)
	}
	args = append(args, image)

	if out, err := d.docker(ctx, host, args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker run: %w: %s", err, strings.TrimSpace(string(out)))
	}

	addr := fmt.Sprintf("%s:%d", addrHost, hostPort)
	if err := waitHTTP(ctx, addr, readyTimeout(spec)); err != nil {
		return nil, fmt.Errorf("wait ready: %w", err)
	}
	return &Instance{Ref: spec.Ref, State: StateRunning, Addr: addr, StartedAt: time.Now()}, nil
}

// readyTimeout is the boot readiness cap: the spec's own value, else 90s.
func readyTimeout(spec Spec) time.Duration {
	if spec.ReadyTimeout > 0 {
		return spec.ReadyTimeout
	}
	return 90 * time.Second
}

func (d *DockerDriver) Start(ctx context.Context, spec Spec) (*Instance, error) {
	host := spec.DockerHost
	d.setHost(spec.Ref, host)
	_, addrHost := placement(d.effHost(host))

	state, port := d.inspect(ctx, host, spec.Ref)
	switch state {
	case StateRunning:
		return &Instance{Ref: spec.Ref, State: StateRunning, Addr: addrHost + ":" + port, StartedAt: time.Now()}, nil
	case StateStopped:
		return d.Create(ctx, spec) // no container yet
	}

	if out, err := d.docker(ctx, host, "start", containerName(spec.Ref)).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker start: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_, port = d.inspect(ctx, host, spec.Ref)
	if port == "" {
		return nil, fmt.Errorf("no published port after start for %s", spec.Ref)
	}
	addr := addrHost + ":" + port
	if err := waitHTTP(ctx, addr, readyTimeout(spec)); err != nil {
		return nil, fmt.Errorf("wait ready: %w", err)
	}
	return &Instance{Ref: spec.Ref, State: StateRunning, Addr: addr, StartedAt: time.Now()}, nil
}

func (d *DockerDriver) Suspend(ctx context.Context, ref string) error {
	return d.docker(ctx, d.hostFor(ref), "stop", containerName(ref)).Run()
}

func (d *DockerDriver) Stop(ctx context.Context, ref string) error {
	return d.docker(ctx, d.hostFor(ref), "rm", "-f", containerName(ref)).Run()
}

func (d *DockerDriver) Status(ctx context.Context, ref string) (State, error) {
	state, _ := d.inspect(ctx, d.hostFor(ref), ref)
	return state, nil
}

// inspect returns the lifecycle state and published host port for a ref on the
// given daemon. A missing container reports StateStopped with an empty port.
func (d *DockerDriver) inspect(ctx context.Context, host, ref string) (State, string) {
	name := containerName(ref)
	out, err := d.docker(ctx, host, "inspect", "-f", "{{.State.Running}}", name).Output()
	if err != nil {
		return StateStopped, ""
	}
	running := strings.TrimSpace(string(out)) == "true"

	portOut, _ := d.docker(ctx, host, "inspect", "-f",
		`{{range $p, $conf := .NetworkSettings.Ports}}{{if $conf}}{{(index $conf 0).HostPort}}{{end}}{{end}}`,
		name).Output()
	port := strings.TrimSpace(string(portOut))

	if running {
		return StateRunning, port
	}
	return StateSuspended, port
}

// waitHTTP blocks until addr returns any HTTP response (not fooled by Docker's
// port-proxy accepting before the app serves).
//
// The probe declares itself a native Expo client. A bare GET / makes an Expo
// dev server produce the WEB target — SSR-rendering the landing page, which
// bundles the entire app for web as the very first thing a cold server does.
// For a workspace whose dependencies diverge from the baked image that build
// gets no cache hits, exceeds the guest's memory, and is OOM-killed — so the
// server boots, builds for a minute, dies, and repeats, and the workload never
// becomes ready even though the app itself is fine. With the header, Expo
// answers with its native manifest: a few milliseconds, no bundling. Servers
// that aren't Expo ignore the headers, and any response still means ready.
func waitHTTP(ctx context.Context, addr string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	url := "http://" + addr + "/"
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
		req.Header.Set("expo-platform", "ios")
		req.Header.Set("Accept", "application/expo+json,application/json")
		resp, err := client.Do(req)
		if err == nil {
			resp.Body.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s: %w", addr, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func (d *DockerDriver) Stats(ctx context.Context, ref string) (Stats, error) {
	out, err := d.docker(ctx, d.hostFor(ref), "stats", "--no-stream", "--format",
		"{{.MemUsage}}|{{.MemPerc}}|{{.CPUPerc}}", containerName(ref)).Output()
	if err != nil {
		return Stats{}, err
	}
	parts := strings.SplitN(strings.TrimSpace(string(out)), "|", 3)
	if len(parts) != 3 {
		return Stats{}, fmt.Errorf("unexpected stats output: %q", string(out))
	}
	return Stats{MemUsage: parts[0], MemPerc: parts[1], CPUPerc: parts[2]}, nil
}

func (d *DockerDriver) Logs(ctx context.Context, ref string, tail int) (string, error) {
	if tail <= 0 {
		tail = 200
	}
	out, _ := d.docker(ctx, d.hostFor(ref), "logs", "--tail", strconv.Itoa(tail), containerName(ref)).CombinedOutput()
	return string(out), nil
}

// ---- image management (runtime.ImageManager) ----

// ListImages returns the non-dangling images on the given daemon.
func (d *DockerDriver) ListImages(ctx context.Context, host string) ([]ImageInfo, error) {
	// Tab-separated so repositories/tags with spaces are impossible to confuse.
	// --no-trunc gives the full config digest as .ID (sha256:<64>); .Digest is the
	// registry manifest digest, present only for images pulled from a registry.
	out, err := d.docker(ctx, host, "images", "--no-trunc", "--filter", "dangling=false",
		"--format", "{{.Repository}}\t{{.Tag}}\t{{.ID}}\t{{.Digest}}\t{{.Size}}\t{{.CreatedSince}}").Output()
	if err != nil {
		return nil, dockerErr(err)
	}
	var imgs []ImageInfo
	for _, line := range strings.Split(strings.TrimSpace(string(out)), "\n") {
		if line == "" {
			continue
		}
		f := strings.Split(line, "\t")
		if len(f) < 6 || f[0] == "<none>" {
			continue // skip untagged/dangling
		}
		fullID := f[2] // "sha256:<64 hex>"
		// Prefer the registry manifest digest as the stable identity; fall back to
		// the config digest (image id) for locally built/loaded images.
		digest := f[3]
		if !strings.HasPrefix(digest, "sha256:") {
			digest = fullID
		}
		imgs = append(imgs, ImageInfo{
			Repository: f[0], Tag: f[1], Ref: f[0] + ":" + f[1],
			ID: shortDigest(fullID), Digest: digest, Size: f[4], CreatedAt: f[5],
		})
	}
	return imgs, nil
}

// shortDigest returns the 12-hex-char short form of a "sha256:<hex>" digest for
// display, leaving anything else untouched.
func shortDigest(d string) string {
	if i := strings.IndexByte(d, ':'); i >= 0 && len(d) >= i+13 {
		return d[i+1 : i+13]
	}
	return d
}

// PullImage pulls ref on the given daemon and returns the combined CLI output.
func (d *DockerDriver) PullImage(ctx context.Context, host, ref string) (string, error) {
	out, err := d.docker(ctx, host, "pull", ref).CombinedOutput()
	if err != nil {
		return string(out), fmt.Errorf("pull %s: %s", ref, firstLine(string(out)))
	}
	return string(out), nil
}

// RemoveImage deletes an image by ref or id on the given daemon.
func (d *DockerDriver) RemoveImage(ctx context.Context, host, ref string, force bool) error {
	args := []string{"rmi"}
	if force {
		args = append(args, "-f")
	}
	args = append(args, ref)
	out, err := d.docker(ctx, host, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s", firstLine(string(out)))
	}
	return nil
}

func dockerErr(err error) error {
	if ee, ok := err.(*exec.ExitError); ok {
		return fmt.Errorf("%s", firstLine(string(ee.Stderr)))
	}
	return err
}

func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		return s[:i]
	}
	if s == "" {
		return "docker command failed"
	}
	return s
}

// docker builds a docker CLI command targeting the given daemon (host), falling
// back to the driver default.
func (d *DockerDriver) docker(ctx context.Context, host string, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = os.Environ()
	if eff := d.effHost(host); eff != "" {
		cmd.Env = append(cmd.Env, "DOCKER_HOST="+eff)
	}
	return cmd
}
