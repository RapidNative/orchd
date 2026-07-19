package runtime

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
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

	_ = d.docker(ctx, host, "rm", "-f", containerName(spec.Ref)).Run()

	if err := os.MkdirAll(spec.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	hostPort, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocate port: %w", err)
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
	args = append(args, image)

	if out, err := d.docker(ctx, host, args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker run: %w: %s", err, strings.TrimSpace(string(out)))
	}

	addr := fmt.Sprintf("%s:%d", addrHost, hostPort)
	if err := waitHTTP(ctx, addr, 90*time.Second); err != nil {
		return nil, fmt.Errorf("wait ready: %w", err)
	}
	return &Instance{Ref: spec.Ref, State: StateRunning, Addr: addr, StartedAt: time.Now()}, nil
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
	if err := waitHTTP(ctx, addr, 90*time.Second); err != nil {
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
func waitHTTP(ctx context.Context, addr string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	url := "http://" + addr + "/"
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		req, _ := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
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
