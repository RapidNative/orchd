package runtime

import (
	"context"
	"fmt"
	"io"
	"log"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	logs  *ringWriter // captured stdout+stderr (last ~64 KiB)
	// dataDir is where this workload's pid file lives, so a stop can clear it.
	dataDir string
}

// ringWriter is a thread-safe writer that keeps only the last max bytes, so a
// long-running process's log stays bounded but the recent tail is available.
type ringWriter struct {
	mu  sync.Mutex
	buf []byte
	max int
}

func (w *ringWriter) Write(p []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	w.buf = append(w.buf, p...)
	if len(w.buf) > w.max {
		w.buf = w.buf[len(w.buf)-w.max:]
	}
	return len(p), nil
}

func (w *ringWriter) String() string {
	w.mu.Lock()
	defer w.mu.Unlock()
	return string(w.buf)
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

	// A pinned HostPort gives stable port-per-workload addressing; otherwise pick
	// a free one.
	port := spec.HostPort
	if port == 0 {
		var err error
		if port, err = freePort(); err != nil {
			return nil, fmt.Errorf("allocate port: %w", err)
		}
	}
	if err := os.MkdirAll(spec.DataDir, 0o700); err != nil {
		return nil, fmt.Errorf("data dir: %w", err)
	}

	// An orchd restart leaves the previous run's workload processes alive (they
	// are not our children any more). Booting on top of one collides: the port
	// is taken, and tinbase's embedded postgres refuses to start against a
	// pgdata whose lock file another postmaster still holds. Clear it first.
	reclaimOrphan(spec.DataDir)

	// Capture output into a bounded ring buffer (for the Logs API) while still
	// surfacing it on the orchestrator's stderr.
	lw := &ringWriter{max: 64 * 1024}
	out := io.MultiWriter(os.Stderr, lw)

	var cmd *exec.Cmd
	label := "workload"
	if spec.TemplateSrc != "" {
		// Template mode: copy the template into DataDir and run its orchd.json
		// workspace (the local, no-Docker path).
		label = "template:" + spec.Workspace
		c, err := d.bootTemplateCmd(spec, port, out)
		if err != nil {
			return nil, err
		}
		cmd = c
	} else {
		// Image/scaffold model: tinbase directly, or a built-in dev-app recipe.
		kind := localKind(spec)
		if kind == "" {
			return nil, fmt.Errorf(
				"local driver has no recipe for image %q (type %q) — use the docker driver",
				spec.Image, spec.Type)
		}
		label = kind
		workDir := spec.WorkDir
		if workDir == "" {
			workDir = spec.DataDir
		}
		if kind == "tinbase" {
			args := []string{
				"start", "--port", fmt.Sprint(port),
				"--dir", workDir, "--data-dir", filepath.Join(spec.DataDir, "db"),
			}
			if d.Engine != "" {
				args = append(args, "--engine", d.Engine)
			}
			cmd = d.command(args) // handles a .js tinbase binary via node
		} else {
			r, _ := recipeFor(kind)
			if err := r.scaffold(workDir); err != nil {
				return nil, fmt.Errorf("scaffold %s: %w", kind, err)
			}
			if err := ensureInstalled(workDir, r.install, out); err != nil {
				return nil, fmt.Errorf("install %s: %w", kind, err)
			}
			argv, extraEnv := r.run(port)
			cmd = exec.Command(argv[0], argv[1:]...)
			cmd.Env = append(os.Environ(), extraEnv...)
		}
		cmd.Dir = workDir
	}

	if cmd.Env == nil {
		cmd.Env = os.Environ()
	}
	for k, v := range spec.Env {
		cmd.Env = append(cmd.Env, k+"="+v)
	}
	cmd.Stdout = out
	cmd.Stderr = out
	setProcAttr(cmd) // own process group, so kill takes the whole tree

	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("start %s: %w", label, err)
	}
	writePidFile(spec.DataDir, cmd.Process.Pid)

	// Dev servers / installs can be slow to first-serve; tinbase is quick.
	addr := fmt.Sprintf("127.0.0.1:%d", port)
	if err := waitReady(ctx, addr, 120*time.Second); err != nil {
		_ = signalTree(cmd.Process.Pid, syscall.SIGKILL)
		removePidFile(spec.DataDir)
		return nil, fmt.Errorf("wait ready: %w", err)
	}

	d.mu.Lock()
	d.procs[spec.Ref] = &localProc{cmd: cmd, port: port, addr: addr, state: StateRunning, logs: lw, dataDir: spec.DataDir}
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
	// Graceful stop of the whole group; tinbase flushes and closes its data dir
	// on SIGINT, and its embedded postgres needs to go with it or the next boot
	// finds a held lock file.
	pid := p.cmd.Process.Pid
	_ = signalTree(pid, syscall.SIGINT)
	done := make(chan struct{})
	go func() { p.cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		_ = signalTree(pid, syscall.SIGKILL)
	}
	// The leader is gone, but grandchildren that ignored SIGINT may not be.
	if processAlive(pid) {
		_ = signalTree(pid, syscall.SIGKILL)
	}
	removePidFile(p.dataDir)
	d.mu.Lock()
	p.state = target
	d.mu.Unlock()
	return nil
}

// pidFile is where a workload's leader pid is recorded, so a later orchd
// process can clean up what this one left running.
func pidFile(dataDir string) string { return filepath.Join(dataDir, ".orchd.pid") }

func writePidFile(dataDir string, pid int) {
	if dataDir == "" {
		return
	}
	_ = os.WriteFile(pidFile(dataDir), []byte(strconv.Itoa(pid)), 0o600)
}

func removePidFile(dataDir string) {
	if dataDir == "" {
		return
	}
	_ = os.Remove(pidFile(dataDir))
}

// reclaimOrphan kills a workload process left over from a previous orchd run.
// Safe to call when there is nothing to reclaim: a missing or stale pid file,
// or a pid that has already exited, are all no-ops.
func reclaimOrphan(dataDir string) {
	if dataDir == "" {
		return
	}
	b, err := os.ReadFile(pidFile(dataDir))
	if err != nil {
		return
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(b)))
	if err != nil || !processAlive(pid) {
		removePidFile(dataDir)
		return
	}
	log.Printf("local: reclaiming orphaned workload process %d (%s)", pid, dataDir)
	_ = signalTree(pid, syscall.SIGINT)
	for i := 0; i < 50 && processAlive(pid); i++ {
		time.Sleep(100 * time.Millisecond)
	}
	if processAlive(pid) {
		_ = signalTree(pid, syscall.SIGKILL)
		time.Sleep(200 * time.Millisecond)
	}
	removePidFile(dataDir)
}

// Stats is unsupported for the process driver (dev only); returns an empty snapshot.
func (d *LocalDriver) Stats(ctx context.Context, ref string) (Stats, error) {
	return Stats{}, nil
}

// Logs returns the last `tail` lines of the workload's captured output.
func (d *LocalDriver) Logs(ctx context.Context, ref string, tail int) (string, error) {
	d.mu.Lock()
	p, ok := d.procs[ref]
	d.mu.Unlock()
	if !ok || p.logs == nil {
		return "", nil
	}
	if tail <= 0 {
		tail = 200
	}
	lines := strings.Split(strings.TrimRight(p.logs.String(), "\n"), "\n")
	if len(lines) > tail {
		lines = lines[len(lines)-tail:]
	}
	return strings.Join(lines, "\n"), nil
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

// ensureInstalled runs an app recipe's one-time setup (e.g. npm install) once
// per workdir, streaming output to w. A marker file records success so an
// install killed midway is retried next boot rather than skipped.
func ensureInstalled(dir string, install []string, w io.Writer) error {
	if len(install) == 0 {
		return nil
	}
	marker := filepath.Join(dir, ".orchd-installed")
	if _, err := os.Stat(marker); err == nil {
		return nil
	}
	fmt.Fprintf(w, "\n[orchd] installing dependencies (%s) — first boot only…\n", strings.Join(install, " "))
	cmd := exec.Command(install[0], install[1:]...)
	cmd.Dir = dir
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Run(); err != nil {
		return err
	}
	return os.WriteFile(marker, []byte("ok\n"), 0o644)
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
