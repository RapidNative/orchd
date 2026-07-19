package runtime

import (
	"context"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"time"
)

// DockerDriver runs each workload as a Docker container, optionally under the
// gVisor (runsc) runtime for VM-grade isolation without KVM. This is the Linux
// substrate for the shared box: it isolates untrusted workloads (RapidNative
// runners, tinbase edge functions) with a userspace kernel, which — unlike
// Firecracker/Kata — does not require nested virtualization.
//
// Suspend/Start map to `docker stop`/`docker start`, so an idle project frees its
// memory while its data (a bind-mounted volume) stays at rest and its published
// port is preserved for a fast wake. The same Runtime interface as LocalDriver,
// so the control plane is unchanged.
type DockerDriver struct {
	// Image is the container image to run (e.g. "tinbase:0.10.0").
	Image string
	// Runtime is the Docker runtime name. "runsc" selects gVisor; empty uses the
	// default (runc, shared kernel).
	Runtime string
	// ContainerPort is the port the workload listens on inside the container.
	ContainerPort int
	// DockerHost, if set, points the docker CLI at a remote daemon
	// (e.g. "ssh://root@host" or "tcp://host:2375"). Empty uses the local daemon.
	DockerHost string
}

// NewDockerDriver builds a DockerDriver. Empty runtime uses runc; pass "runsc"
// for gVisor isolation.
func NewDockerDriver(image, dockerRuntime string) *DockerDriver {
	return &DockerDriver{
		Image:         image,
		Runtime:       dockerRuntime,
		ContainerPort: 54321,
	}
}

func (d *DockerDriver) Name() string {
	if d.Runtime != "" {
		return "docker+" + d.Runtime
	}
	return "docker"
}

func containerName(ref string) string { return "tb-" + ref }

func (d *DockerDriver) Create(ctx context.Context, spec Spec) (*Instance, error) {
	// A fresh provision: remove any stale container with this name, then run.
	_ = d.docker(ctx, "rm", "-f", containerName(spec.Ref)).Run()

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
		"-p", fmt.Sprintf("127.0.0.1:%d:%d", hostPort, cport),
		"-v", spec.DataDir + ":/data",
	}
	if d.Runtime != "" {
		args = append(args, "--runtime", d.Runtime)
	}
	// Resource caps: one tenant cannot starve the host. --memory-swap == --memory
	// disables swap so the cap is hard; exceeding it OOM-kills the container.
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

	if out, err := d.docker(ctx, args...).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker run: %w: %s", err, strings.TrimSpace(string(out)))
	}

	addr := fmt.Sprintf("127.0.0.1:%d", hostPort)
	if err := waitHTTP(ctx, addr, 90*time.Second); err != nil {
		return nil, fmt.Errorf("wait ready: %w", err)
	}
	return &Instance{Ref: spec.Ref, State: StateRunning, Addr: addr, StartedAt: time.Now()}, nil
}

func (d *DockerDriver) Start(ctx context.Context, spec Spec) (*Instance, error) {
	state, port := d.inspect(ctx, spec.Ref)
	switch state {
	case StateRunning:
		return &Instance{Ref: spec.Ref, State: StateRunning, Addr: "127.0.0.1:" + port, StartedAt: time.Now()}, nil
	case StateStopped:
		// No container exists yet — provision one.
		return d.Create(ctx, spec)
	}

	// Exists but stopped/suspended: start it. Docker preserves the port mapping.
	if out, err := d.docker(ctx, "start", containerName(spec.Ref)).CombinedOutput(); err != nil {
		return nil, fmt.Errorf("docker start: %w: %s", err, strings.TrimSpace(string(out)))
	}
	_, port = d.inspect(ctx, spec.Ref)
	if port == "" {
		return nil, fmt.Errorf("no published port after start for %s", spec.Ref)
	}
	addr := "127.0.0.1:" + port
	if err := waitHTTP(ctx, addr, 90*time.Second); err != nil {
		return nil, fmt.Errorf("wait ready: %w", err)
	}
	return &Instance{Ref: spec.Ref, State: StateRunning, Addr: addr, StartedAt: time.Now()}, nil
}

func (d *DockerDriver) Suspend(ctx context.Context, ref string) error {
	// Stop frees memory but keeps the container + volume for a fast resume.
	return d.docker(ctx, "stop", containerName(ref)).Run()
}

func (d *DockerDriver) Stop(ctx context.Context, ref string) error {
	return d.docker(ctx, "rm", "-f", containerName(ref)).Run()
}

func (d *DockerDriver) Status(ctx context.Context, ref string) (State, error) {
	state, _ := d.inspect(ctx, ref)
	return state, nil
}

// inspect returns the lifecycle state and published host port for a ref. A
// missing container reports StateStopped with an empty port.
func (d *DockerDriver) inspect(ctx context.Context, ref string) (State, string) {
	name := containerName(ref)
	out, err := d.docker(ctx, "inspect", "-f", "{{.State.Running}}", name).Output()
	if err != nil {
		return StateStopped, "" // no such container
	}
	running := strings.TrimSpace(string(out)) == "true"

	// Read the published host port generically (each container publishes exactly
	// one port), so inspect need not know the container's internal port.
	portOut, _ := d.docker(ctx, "inspect", "-f",
		`{{range $p, $conf := .NetworkSettings.Ports}}{{if $conf}}{{(index $conf 0).HostPort}}{{end}}{{end}}`,
		name).Output()
	port := strings.TrimSpace(string(portOut))

	if running {
		return StateRunning, port
	}
	return StateSuspended, port
}

// waitHTTP blocks until addr returns any HTTP response. Unlike a raw TCP dial it
// is not fooled by Docker's port-proxy, which accepts connections on the
// published port before the container's process is actually serving.
func waitHTTP(ctx context.Context, addr string, timeout time.Duration) error {
	client := &http.Client{Timeout: 2 * time.Second}
	// Any HTTP reply (even 404) means the server is up; probe root so this stays
	// workload-agnostic rather than tinbase-specific.
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
			return nil // a real HTTP reply means the app is up
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s: %w", addr, err)
		}
		time.Sleep(300 * time.Millisecond)
	}
}

func (d *DockerDriver) docker(ctx context.Context, args ...string) *exec.Cmd {
	cmd := exec.CommandContext(ctx, "docker", args...)
	cmd.Env = os.Environ()
	if d.DockerHost != "" {
		cmd.Env = append(cmd.Env, "DOCKER_HOST="+d.DockerHost)
	}
	return cmd
}
