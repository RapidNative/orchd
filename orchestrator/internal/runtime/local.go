package runtime

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"time"
)

// LocalDriver runs each workload as an ordinary OS process on the host. It is the
// development driver: it lets us build and exercise the entire control plane,
// gateway, wake sequencing, and backup paths on macOS, where Firecracker (which
// needs Linux/KVM) cannot run. The FirecrackerDriver will implement the same
// interface on Linux bare metal.
//
// For a tinbase project the process is `tinbase start` pointed at a per-project
// data dir. Suspend/Start here are a plain kill/relaunch (tinbase reopens its
// data dir on boot, ~2s); the sub-second snapshot resume is a FirecrackerDriver
// concern, deliberately hidden behind Start().
type LocalDriver struct {
	// Binary is the tinbase executable (single binary or a wrapper). If it looks
	// like a .js file it is run via `node`.
	Binary string
	// Engine selects the tinbase engine (native|wasm|pgmem). Empty uses tinbase's
	// default (native on macOS/Linux).
	Engine string

	mu    sync.Mutex
	procs map[string]*localProc
}

type localProc struct {
	cmd   *exec.Cmd
	port  int
	addr  string
	state State
}

// NewLocalDriver constructs a LocalDriver. binary is the path to the tinbase
// executable the driver will spawn.
func NewLocalDriver(binary, engine string) *LocalDriver {
	return &LocalDriver{
		Binary: binary,
		Engine: engine,
		procs:  make(map[string]*localProc),
	}
}

func (d *LocalDriver) Name() string { return "local" }

func (d *LocalDriver) Create(ctx context.Context, spec Spec) (*Instance, error) {
	return d.boot(ctx, spec)
}

func (d *LocalDriver) Start(ctx context.Context, spec Spec) (*Instance, error) {
	return d.boot(ctx, spec)
}

// boot launches tinbase for a ref and waits until it accepts connections.
func (d *LocalDriver) boot(ctx context.Context, spec Spec) (*Instance, error) {
	d.mu.Lock()
	if p, ok := d.procs[spec.Ref]; ok && p.state == StateRunning {
		inst := &Instance{Ref: spec.Ref, State: StateRunning, Addr: p.addr, StartedAt: time.Now()}
		d.mu.Unlock()
		return inst, nil
	}
	d.mu.Unlock()

	port, err := freePort()
	if err != nil {
		return nil, fmt.Errorf("allocate port: %w", err)
	}

	if err := os.MkdirAll(spec.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}
	workDir := spec.WorkDir
	if workDir == "" {
		workDir = spec.DataDir
	}

	args := []string{
		"start",
		"--port", fmt.Sprint(port),
		"--dir", workDir,
		"--data-dir", filepath.Join(spec.DataDir, "db"),
	}
	if d.Engine != "" {
		args = append(args, "--engine", d.Engine)
	}

	cmd := d.command(args)
	cmd.Dir = workDir
	cmd.Env = os.Environ()
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	// Surface tinbase logs on the orchestrator's stderr for now; a real deployment
	// would ship these to a per-project log sink.
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start tinbase: %w", err)
	}

	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := waitReady(ctx, addr, 30*time.Second); err != nil {
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("wait ready: %w", err)
	}

	d.mu.Lock()
	d.procs[spec.Ref] = &localProc{cmd: cmd, port: port, addr: addr, state: StateRunning}
	d.mu.Unlock()

	// Reap the process if it exits on its own so state does not go stale.
	go func() {
		_ = cmd.Wait()
		d.mu.Lock()
		if p, ok := d.procs[spec.Ref]; ok && p.cmd == cmd {
			p.state = StateStopped
		}
		d.mu.Unlock()
	}()

	return &Instance{Ref: spec.Ref, State: StateRunning, Addr: addr, StartedAt: time.Now()}, nil
}

func (d *LocalDriver) Suspend(ctx context.Context, ref string) error {
	return d.kill(ref, StateSuspended)
}

func (d *LocalDriver) Stop(ctx context.Context, ref string) error {
	return d.kill(ref, StateStopped)
}

func (d *LocalDriver) kill(ref string, target State) error {
	d.mu.Lock()
	p, ok := d.procs[ref]
	d.mu.Unlock()
	if !ok || p.cmd.Process == nil {
		return nil
	}
	// Graceful stop; tinbase flushes and closes its data dir on SIGTERM.
	_ = p.cmd.Process.Signal(os.Interrupt)
	done := make(chan struct{})
	go func() { p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = p.cmd.Process.Kill()
	}
	d.mu.Lock()
	p.state = target
	d.mu.Unlock()
	return nil
}

// Stats is unsupported for the process driver (dev only); returns an empty snapshot.
func (d *LocalDriver) Stats(ctx context.Context, ref string) (Stats, error) {
	return Stats{}, nil
}

// Logs is unsupported for the process driver (output goes to orchd's stderr).
func (d *LocalDriver) Logs(ctx context.Context, ref string, tail int) (string, error) {
	return "", nil
}

func (d *LocalDriver) Status(ctx context.Context, ref string) (State, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	p, ok := d.procs[ref]
	if !ok {
		return StateStopped, nil
	}
	return p.state, nil
}

// command builds the exec.Cmd, running .js binaries under node.
func (d *LocalDriver) command(args []string) *exec.Cmd {
	if filepath.Ext(d.Binary) == ".js" {
		return exec.Command("node", append([]string{d.Binary}, args...)...)
	}
	return exec.Command(d.Binary, args...)
}

// freePort asks the OS for an unused TCP port.
func freePort() (int, error) {
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return 0, err
	}
	defer l.Close()
	return l.Addr().(*net.TCPAddr).Port, nil
}

// waitReady blocks until addr accepts a TCP connection or the deadline passes.
func waitReady(ctx context.Context, addr string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		conn, err := net.DialTimeout("tcp", addr, 500*time.Millisecond)
		if err == nil {
			conn.Close()
			return nil
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("timed out waiting for %s", addr)
		}
		time.Sleep(150 * time.Millisecond)
	}
}
