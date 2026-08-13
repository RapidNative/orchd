//go:build linux

// FirecrackerDriver runs each workload in a Firecracker microVM: rootfs is a
// dm-thin clone of the image's warm base volume (CoW — a project's disk costs
// ~zero), each VM lives in its own network namespace with a uniform guest IP
// (which makes memory snapshots portable), and scale-to-zero is a memory
// snapshot: Suspend pauses + snapshots + kills; Start restores in ~100ms with
// the dev server's caches and bundle still in RAM.
//
// Spike numbers behind the design (docs/firecracker-snapshots.md): fresh boot
// to serving 6-7s, snapshot restore to serving 61-81ms, thin clone 47ms.
package runtime

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"
)

type FirecrackerConfig struct {
	Bin        string // firecracker binary
	Kernel     string // uncompressed vmlinux
	Root       string // driver state root, e.g. /opt/orchd/data/fc
	Pool       string // dm-thin pool name, e.g. fcpool
	VolumeGiB  int64  // virtual size of each rootfs volume
	VcpuCount  int
	MemSizeMiB int
	GuestPort  int // the workload's in-guest listen port
	AgentPort  int // in-guest agent port
}

func (c *FirecrackerConfig) defaults() {
	if c.VolumeGiB == 0 {
		c.VolumeGiB = 8
	}
	if c.VcpuCount == 0 {
		c.VcpuCount = 4
	}
	if c.MemSizeMiB == 0 {
		c.MemSizeMiB = 3072
	}
	if c.GuestPort == 0 {
		c.GuestPort = 8080
	}
	if c.AgentPort == 0 {
		c.AgentPort = 9000
	}
}

type FirecrackerDriver struct {
	cfg  FirecrackerConfig
	pool *thinPool

	mu  sync.Mutex
	idx map[string]*vmMeta // ref -> loaded meta (also persisted per VM)
}

// vmMeta is the durable per-VM record (vms/<ref>/meta.json): everything needed
// to find the VM again after a driver restart.
type vmMeta struct {
	Ref    string `json:"ref"`
	DevID  int    `json:"dev_id"`  // thin volume device id
	NetIdx int    `json:"net_idx"` // veth /30 index
	Image  string `json:"image"`   // fc image name (template@version)
	PID    int    `json:"pid"`     // firecracker pid when running (0 otherwise)
}

func NewFirecrackerDriver(cfg FirecrackerConfig) (*FirecrackerDriver, error) {
	cfg.defaults()
	if cfg.Bin == "" || cfg.Kernel == "" || cfg.Root == "" || cfg.Pool == "" {
		return nil, fmt.Errorf("firecracker driver: Bin, Kernel, Root and Pool are required")
	}
	if err := os.MkdirAll(filepath.Join(cfg.Root, "vms"), 0o755); err != nil {
		return nil, err
	}
	d := &FirecrackerDriver{
		cfg:  cfg,
		pool: &thinPool{Name: cfg.Pool, root: cfg.Root, sectors: cfg.VolumeGiB * 1024 * 1024 * 1024 / 512},
		idx:  map[string]*vmMeta{},
	}
	return d, nil
}

func (d *FirecrackerDriver) Name() string { return "firecracker" }

func (d *FirecrackerDriver) vmDir(ref string) string { return filepath.Join(d.cfg.Root, "vms", ref) }
func (d *FirecrackerDriver) dmName(ref string) string {
	return "fcvm-" + sanitizeNet(ref) + "-rootfs"
}

func (d *FirecrackerDriver) meta(ref string) (*vmMeta, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if m, ok := d.idx[ref]; ok {
		return m, nil
	}
	b, err := os.ReadFile(filepath.Join(d.vmDir(ref), "meta.json"))
	if err != nil {
		return nil, err
	}
	m := &vmMeta{}
	if err := json.Unmarshal(b, m); err != nil {
		return nil, err
	}
	d.idx[ref] = m
	return m, nil
}

func (d *FirecrackerDriver) saveMeta(m *vmMeta) error {
	d.mu.Lock()
	d.idx[m.Ref] = m
	d.mu.Unlock()
	b, _ := json.MarshalIndent(m, "", " ")
	return os.WriteFile(filepath.Join(d.vmDir(m.Ref), "meta.json"), b, 0o644)
}

// ---- lifecycle ----

// Create provisions a new VM: thin-clone the image's warm base volume, write
// the workload env into the clone, fresh-boot it in its own namespace, and
// wait until the workload serves.
func (d *FirecrackerDriver) Create(ctx context.Context, spec Spec) (*Instance, error) {
	img, err := d.imageMeta(spec.Image)
	if err != nil {
		return nil, fmt.Errorf("fc image %q: %w (run image prep first)", spec.Image, err)
	}
	if err := os.MkdirAll(d.vmDir(spec.Ref), 0o755); err != nil {
		return nil, err
	}
	devID, err := d.pool.nextDevID()
	if err != nil {
		return nil, err
	}
	if err := d.pool.snapshotOf(img.WarmDevID, devID, d.dmName(spec.Ref)); err != nil {
		return nil, fmt.Errorf("clone rootfs: %w", err)
	}
	m := &vmMeta{Ref: spec.Ref, DevID: devID, NetIdx: devID, Image: spec.Image}
	if err := d.saveMeta(m); err != nil {
		return nil, err
	}
	if err := d.writeGuestEnv(m, spec.Env); err != nil {
		return nil, fmt.Errorf("write guest env: %w", err)
	}
	inst, err := d.boot(ctx, m, spec, false)
	if err != nil {
		return nil, err
	}
	inst.State = StateRunning
	return inst, nil
}

// Start wakes a suspended VM. With a snapshot present this is a memory
// restore (~100ms); otherwise it falls back to a fresh boot.
func (d *FirecrackerDriver) Start(ctx context.Context, spec Spec) (*Instance, error) {
	m, err := d.meta(spec.Ref)
	if err != nil {
		return d.Create(ctx, spec)
	}
	if st, _ := d.Status(ctx, spec.Ref); st == StateRunning {
		n := &vmNet{Ref: spec.Ref, Idx: m.NetIdx}
		return &Instance{Ref: spec.Ref, State: StateRunning, Addr: n.Addr(d.cfg.GuestPort), StartedAt: time.Now()}, nil
	}
	if _, err := os.Stat(d.snapPath(spec.Ref, "mem")); err == nil {
		inst, err := d.restore(ctx, m)
		if err == nil {
			return inst, nil
		}
		// a failed restore must not wedge the workload: drop the snapshot
		// and take the fresh-boot path (slow wake beats no wake)
		_ = os.Remove(d.snapPath(spec.Ref, "mem"))
		_ = os.Remove(d.snapPath(spec.Ref, "state"))
	}
	return d.boot(ctx, m, spec, false)
}

// Suspend scales to zero: pause the VM, snapshot memory+state next to the
// rootfs clone, kill the VMM. The next Start is a restore.
func (d *FirecrackerDriver) Suspend(ctx context.Context, ref string) error {
	m, err := d.meta(ref)
	if err != nil {
		return err
	}
	if m.PID == 0 || !pidAlive(m.PID) {
		return nil // already down
	}
	if err := d.api(ref, "PATCH", "vm", `{"state":"Paused"}`); err != nil {
		return fmt.Errorf("pause: %w", err)
	}
	body := fmt.Sprintf(`{"snapshot_type":"Full","snapshot_path":%q,"mem_file_path":%q}`,
		d.snapPath(ref, "state"), d.snapPath(ref, "mem"))
	if err := d.api(ref, "PUT", "snapshot/create", body); err != nil {
		return fmt.Errorf("snapshot: %w", err)
	}
	d.kill(m)
	return nil
}

// Stop halts without a snapshot; the next Start is a fresh boot.
func (d *FirecrackerDriver) Stop(ctx context.Context, ref string) error {
	m, err := d.meta(ref)
	if err != nil {
		return nil
	}
	d.kill(m)
	_ = os.Remove(d.snapPath(ref, "mem"))
	_ = os.Remove(d.snapPath(ref, "state"))
	return nil
}

// Delete releases everything: VMM, namespace, thin volume, state dir.
// Not part of the Runtime interface; the manager reaches it via assertion,
// mirroring RemoveVolumes on the docker driver.
func (d *FirecrackerDriver) Delete(ctx context.Context, ref string) error {
	m, err := d.meta(ref)
	if err != nil {
		return nil
	}
	d.kill(m)
	(&vmNet{Ref: ref, Idx: m.NetIdx}).destroy()
	_ = d.pool.remove(m.DevID, d.dmName(ref))
	d.mu.Lock()
	delete(d.idx, ref)
	d.mu.Unlock()
	return os.RemoveAll(d.vmDir(ref))
}

func (d *FirecrackerDriver) Status(ctx context.Context, ref string) (State, error) {
	m, err := d.meta(ref)
	if err != nil {
		return StateStopped, nil
	}
	if m.PID != 0 && pidAlive(m.PID) {
		return StateRunning, nil
	}
	if _, err := os.Stat(d.snapPath(ref, "mem")); err == nil {
		return StateSuspended, nil
	}
	return StateStopped, nil
}

func (d *FirecrackerDriver) Stats(ctx context.Context, ref string) (Stats, error) {
	m, err := d.meta(ref)
	if err != nil || m.PID == 0 || !pidAlive(m.PID) {
		return Stats{}, fmt.Errorf("not running")
	}
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/status", m.PID))
	if err != nil {
		return Stats{}, err
	}
	rss := "?"
	for _, l := range strings.Split(string(b), "\n") {
		if strings.HasPrefix(l, "VmRSS:") {
			if kb, err := strconv.Atoi(strings.TrimSpace(strings.TrimSuffix(strings.TrimPrefix(l, "VmRSS:"), "kB"))); err == nil {
				rss = fmt.Sprintf("%.1fMiB", float64(kb)/1024)
			}
		}
	}
	return Stats{MemUsage: fmt.Sprintf("%s / %dMiB", rss, d.cfg.MemSizeMiB), MemPerc: "", CPUPerc: ""}, nil
}

// Logs proxies to the in-guest agent (the dev server's output lives inside
// the VM). A stopped VM has no logs to give.
func (d *FirecrackerDriver) Logs(ctx context.Context, ref string, tail int) (string, error) {
	m, err := d.meta(ref)
	if err != nil {
		return "", err
	}
	n := &vmNet{Ref: ref, Idx: m.NetIdx}
	res, err := http.Get(fmt.Sprintf("http://%s/logs?tail=%d", n.Addr(d.cfg.AgentPort), tail))
	if err != nil {
		return "", fmt.Errorf("agent: %w", err)
	}
	defer res.Body.Close()
	b, _ := io.ReadAll(res.Body)
	return string(b), nil
}

// WriteFile sends one file into the guest via the agent. The manager uses
// this (by assertion) instead of host-side workspace writes; callers must
// ensure the VM is running first — that is the wake-on-write rule.
func (d *FirecrackerDriver) WriteFile(ctx context.Context, ref, path string, content []byte) error {
	m, err := d.meta(ref)
	if err != nil {
		return err
	}
	n := &vmNet{Ref: ref, Idx: m.NetIdx}
	body, _ := json.Marshal(map[string]string{"p": path, "b64": encodeB64(content)})
	req, _ := http.NewRequestWithContext(ctx, "PUT", fmt.Sprintf("http://%s/file", n.Addr(d.cfg.AgentPort)), bytes.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return fmt.Errorf("agent: %w", err)
	}
	defer res.Body.Close()
	if res.StatusCode != 200 {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("agent write %s: %s: %s", path, res.Status, string(b))
	}
	return nil
}

// ---- boot / restore internals ----

func (d *FirecrackerDriver) snapPath(ref, kind string) string {
	return filepath.Join(d.vmDir(ref), "snap."+kind)
}
func (d *FirecrackerDriver) sockPath(ref string) string {
	return filepath.Join(d.vmDir(ref), "fc.sock")
}

// spawn launches the firecracker VMM inside the VM's namespace with the VM
// dir as cwd (the rootfs symlink is relative, which is what lets one template
// snapshot restore onto any clone).
func (d *FirecrackerDriver) spawn(m *vmMeta) error {
	dir := d.vmDir(m.Ref)
	_ = os.Remove(d.sockPath(m.Ref))
	if err := os.Symlink("/dev/mapper/"+d.dmName(m.Ref), filepath.Join(dir, "rootfs.blk")); err != nil && !os.IsExist(err) {
		return err
	}
	logf, err := os.OpenFile(filepath.Join(dir, "fc.log"), os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0o644)
	if err != nil {
		return err
	}
	defer logf.Close()
	n := &vmNet{Ref: m.Ref, Idx: m.NetIdx}
	if err := n.ensure(); err != nil {
		return fmt.Errorf("netns: %w", err)
	}
	cmd := exec.Command("ip", "netns", "exec", n.ns(), d.cfg.Bin, "--api-sock", d.sockPath(m.Ref))
	cmd.Dir = dir
	cmd.Stdout, cmd.Stderr = logf, logf
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap
	m.PID = cmd.Process.Pid
	if err := d.saveMeta(m); err != nil {
		return err
	}
	// wait for the API socket
	for i := 0; i < 50; i++ {
		if _, err := os.Stat(d.sockPath(m.Ref)); err == nil {
			return nil
		}
		time.Sleep(20 * time.Millisecond)
	}
	return fmt.Errorf("firecracker api socket never appeared")
}

func (d *FirecrackerDriver) boot(ctx context.Context, m *vmMeta, spec Spec, _ bool) (*Instance, error) {
	if err := d.spawn(m); err != nil {
		return nil, err
	}
	bootArgs := fmt.Sprintf(
		"console=ttyS0 reboot=k panic=1 pci=off init=/sbin/orchd-init ip=%s::%s:255.255.255.240::eth0:off",
		fcGuestIP, fcTapIP)
	steps := []struct{ path, body string }{
		{"boot-source", fmt.Sprintf(`{"kernel_image_path":%q,"boot_args":%q}`, d.cfg.Kernel, bootArgs)},
		{"drives/rootfs", `{"drive_id":"rootfs","path_on_host":"rootfs.blk","is_root_device":true,"is_read_only":false}`},
		{"network-interfaces/eth0", fmt.Sprintf(`{"iface_id":"eth0","guest_mac":"06:00:AC:10:00:02","host_dev_name":%q}`, fcTapName)},
		{"machine-config", fmt.Sprintf(`{"vcpu_count":%d,"mem_size_mib":%d}`, d.cfg.VcpuCount, d.cfg.MemSizeMiB)},
		{"actions", `{"action_type":"InstanceStart"}`},
	}
	for _, s := range steps {
		if err := d.api(m.Ref, "PUT", s.path, s.body); err != nil {
			d.kill(m)
			return nil, fmt.Errorf("fc %s: %w", s.path, err)
		}
	}
	n := &vmNet{Ref: m.Ref, Idx: m.NetIdx}
	addr := n.Addr(d.cfg.GuestPort)
	if err := waitHTTP(ctx, addr, readyTimeout(spec)); err != nil {
		d.kill(m)
		return nil, fmt.Errorf("wait ready: %w", err)
	}
	return &Instance{Ref: m.Ref, State: StateRunning, Addr: addr, StartedAt: time.Now()}, nil
}

func (d *FirecrackerDriver) restore(ctx context.Context, m *vmMeta) (*Instance, error) {
	if err := d.spawn(m); err != nil {
		return nil, err
	}
	body := fmt.Sprintf(`{"snapshot_path":%q,"mem_backend":{"backend_type":"File","backend_path":%q},"resume_vm":true}`,
		d.snapPath(m.Ref, "state"), d.snapPath(m.Ref, "mem"))
	if err := d.api(m.Ref, "PUT", "snapshot/load", body); err != nil {
		d.kill(m)
		return nil, fmt.Errorf("snapshot load: %w", err)
	}
	n := &vmNet{Ref: m.Ref, Idx: m.NetIdx}
	addr := n.Addr(d.cfg.GuestPort)
	if err := waitHTTP(ctx, addr, 15*time.Second); err != nil {
		d.kill(m)
		return nil, fmt.Errorf("wait ready after restore: %w", err)
	}
	// the snapshot is consumed; a crash before the next Suspend must fall
	// back to a fresh boot rather than restoring doubly-stale memory
	_ = os.Remove(d.snapPath(m.Ref, "mem"))
	_ = os.Remove(d.snapPath(m.Ref, "state"))
	return &Instance{Ref: m.Ref, State: StateRunning, Addr: addr, StartedAt: time.Now()}, nil
}

func (d *FirecrackerDriver) kill(m *vmMeta) {
	if m.PID != 0 && pidAlive(m.PID) {
		if p, err := os.FindProcess(m.PID); err == nil {
			_ = p.Kill()
		}
		// wait for the VMM to actually die: it holds the tap fd, and a
		// respawn that races the teardown gets EBUSY opening tap0 (seen on
		// suspend immediately followed by Start).
		for i := 0; i < 150 && pidAlive(m.PID); i++ {
			time.Sleep(20 * time.Millisecond)
		}
	}
	m.PID = 0
	_ = d.saveMeta(m)
}

// api calls the firecracker unix-socket HTTP API for a VM.
func (d *FirecrackerDriver) api(ref, method, path, body string) error {
	client := http.Client{
		Transport: &http.Transport{DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
			return net.Dial("unix", d.sockPath(ref))
		}},
		Timeout: 60 * time.Second,
	}
	req, err := http.NewRequest(method, "http://fc/"+path, strings.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := client.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if res.StatusCode >= 300 {
		b, _ := io.ReadAll(res.Body)
		return fmt.Errorf("%s: %s", res.Status, strings.TrimSpace(string(b)))
	}
	return nil
}

// writeGuestEnv mounts the VM's rootfs clone and drops the workload env where
// the baked init sources it. Only valid while the VM is down.
func (d *FirecrackerDriver) writeGuestEnv(m *vmMeta, env map[string]string) error {
	mnt := filepath.Join(d.vmDir(m.Ref), "mnt")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return err
	}
	if err := sh("mount", "/dev/mapper/"+d.dmName(m.Ref), mnt); err != nil {
		return err
	}
	defer sh("umount", mnt)
	var b strings.Builder
	for k, v := range env {
		b.WriteString(fmt.Sprintf("export %s=%q\n", k, v))
	}
	return os.WriteFile(filepath.Join(mnt, "etc", "orchd.env"), []byte(b.String()), 0o644)
}

func pidAlive(pid int) bool {
	err := sh("kill", "-0", strconv.Itoa(pid))
	return err == nil
}
