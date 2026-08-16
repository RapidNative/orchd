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
	"log"
	"net"
	"net/http"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"syscall"
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
	// MaxLive caps concurrently running microVMs. Each guest reserves its full
	// MemSizeMiB, so an uncapped fleet fills host memory and the box thrashes:
	// wakes slow down, snapshot-based suspends start failing, which keeps even
	// more VMs alive — observed 2026-08-14, 93 live VMs on a 125GB host, load
	// 520. 0 disables the cap.
	MaxLive int
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
	if c.MaxLive == 0 {
		// Half of host memory for microVMs. Two-thirds was tried and melted the
		// box: the "remaining third" also has to hold every container (db/api/
		// web for the same fleet), Metro's bundling spikes inside guests pinning
		// their full reservation, and the page cache. 2026-08-16: 27 VMs under
		// the 2/3 budget plus ~90 containers took a 125GB host to OOM and 30+
		// minutes of unreachability.
		if total := hostMemMiB(); total > 0 && c.MemSizeMiB > 0 {
			c.MaxLive = (total / 2) / c.MemSizeMiB
		}
		if c.MaxLive < 4 {
			c.MaxLive = 4
		}
	}
}

// hostMemMiB reads total system memory; 0 when it cannot be determined.
func hostMemMiB() int {
	b, err := os.ReadFile("/proc/meminfo")
	if err != nil {
		return 0
	}
	for _, line := range strings.Split(string(b), "\n") {
		if !strings.HasPrefix(line, "MemTotal:") {
			continue
		}
		f := strings.Fields(line)
		if len(f) < 2 {
			return 0
		}
		kb, err := strconv.Atoi(f[1])
		if err != nil {
			return 0
		}
		return kb / 1024
	}
	return 0
}

type FirecrackerDriver struct {
	cfg  FirecrackerConfig
	pool *thinPool

	mu  sync.Mutex
	idx map[string]*vmMeta // ref -> loaded meta (also persisted per VM)
	// refLocks serialize snapshot compression against wakes: Suspend hands
	// the mem file to an async zstd; a Start racing it must wait for whichever
	// form (raw or .zst) wins.
	refLocks sync.Map // ref -> *sync.Mutex
	// bootSem staggers fresh boots: N concurrent cold boots through the thin
	// pool serialize IO badly enough to blow ReadyTimeout and trigger create
	// fallbacks (S2's load-735 lesson, re-observed with live traffic).
	// Snapshot restores are cheap and skip this.
	bootSem chan struct{}
	// pendingSpawns counts spawns that have been admitted but have not yet
	// recorded a PID — the window in which a live-VM count undercounts.
	// Admission adds pending to the count so a burst of concurrent spawns
	// cannot all pass the capacity check together (that race is how a 15-wide
	// provision herd sailed past MaxLive and exhausted host memory).
	//
	// Guarded by its OWN mutex, never d.mu: the capacity count reads VM metas,
	// and meta() takes d.mu — admission holding d.mu while counting deadlocked
	// the whole driver on its first spawn (every wake on the box queued behind
	// one self-waiting goroutine; 2026-08-16 evening, the third outage of the
	// day and the only one with a one-line cause).
	admitMu       sync.Mutex
	pendingSpawns int
}

func (d *FirecrackerDriver) lockFor(ref string) *sync.Mutex {
	lk, _ := d.refLocks.LoadOrStore(ref, &sync.Mutex{})
	return lk.(*sync.Mutex)
}

// vmMeta is the durable per-VM record (vms/<ref>/meta.json): everything needed
// to find the VM again after a driver restart.
type vmMeta struct {
	Ref    string `json:"ref"`
	DevID  int    `json:"dev_id"`  // thin volume device id
	NetIdx int    `json:"net_idx"` // veth /30 index
	Image  string `json:"image"`   // fc image name (template@version)
	PID    int    `json:"pid"`     // firecracker pid when running (0 otherwise)
	// Ephemeral: suspend = teardown (no snapshot); wake = fresh boot.
	Ephemeral bool `json:"ephemeral,omitempty"`
	// Deps names the shared read-only node_modules family volume attached as
	// the VM's second drive (empty = no deps drive; boots as before). Recorded
	// so spawn/restore can recreate the deps.blk symlink — a memory snapshot
	// restores with its drive set and needs the file present.
	Deps string `json:"deps,omitempty"`
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
		cfg:     cfg,
		pool:    &thinPool{Name: cfg.Pool, root: cfg.Root, sectors: cfg.VolumeGiB * 1024 * 1024 * 1024 / 512},
		idx:     map[string]*vmMeta{},
		bootSem: make(chan struct{}, 3),
	}
	// Storage is in-kernel state: after a reboot the pool table and every
	// volume mapping are gone while their backing files remain. Rebuild them at
	// startup so workloads wake normally instead of failing on a missing rootfs
	// device — and so the box needs no manual step after a restart.
	if err := d.ensureStorage(); err != nil {
		log.Printf("firecracker: storage reactivation incomplete: %v", err)
	}
	return d, nil
}

// ensureStorage reactivates the thin pool and every known volume (image
// base/warm volumes first, then per-VM rootfs clones). Idempotent: activation
// is skipped for anything already mapped, so it is cheap on a normal restart.
func (d *FirecrackerDriver) ensureStorage() error {
	if err := d.pool.ensurePool(); err != nil {
		return err
	}
	activated, failed := 0, 0
	imagesDir := filepath.Join(d.cfg.Root, "images")
	if entries, err := os.ReadDir(imagesDir); err == nil {
		for _, e := range entries {
			im, err := d.imageMeta(e.Name())
			if err != nil {
				continue
			}
			for devID, dmName := range map[int]string{
				im.BaseDevID: "fcimg-" + sanitizeTagFC(im.Name) + "-base",
				im.WarmDevID: "fcimg-" + sanitizeTagFC(im.Name) + "-warm",
			} {
				if _, err := os.Stat("/dev/mapper/" + dmName); err == nil {
					continue
				}
				if err := d.pool.activate(devID, dmName); err != nil {
					failed++
				} else {
					activated++
				}
			}
		}
	}
	vmsDir := filepath.Join(d.cfg.Root, "vms")
	entries, err := os.ReadDir(vmsDir)
	if err != nil {
		return nil
	}
	for _, e := range entries {
		m, err := d.meta(e.Name())
		if err != nil {
			continue
		}
		dmName := d.dmName(m.Ref)
		if _, err := os.Stat("/dev/mapper/" + dmName); err == nil {
			continue
		}
		if err := d.pool.activate(m.DevID, dmName); err != nil {
			failed++
		} else {
			activated++
		}
	}
	if activated > 0 || failed > 0 {
		log.Printf("firecracker: reactivated %d volume(s), %d failed", activated, failed)
	}
	return nil
}

func (d *FirecrackerDriver) Name() string { return "firecracker" }

// Knows reports whether this driver owns a ref (durable: meta.json survives
// restarts). The Mux routes every ref-keyed call with it.
func (d *FirecrackerDriver) Knows(ref string) bool {
	_, err := d.meta(ref)
	return err == nil
}

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
	img, err := d.imageMeta(sanitizeTagFC(spec.Image))
	if err != nil {
		return nil, fmt.Errorf("fc image %q: %w (run image prep first)", spec.Image, err)
	}
	// A ref being re-created (retry after a failed attempt, project reset)
	// must not inherit half-made state — stale clones, snapshots or netns
	// wreck the new instance in confusing ways. Start from nothing.
	if d.Knows(spec.Ref) {
		_ = d.Delete(ctx, spec.Ref)
	}
	if err := os.MkdirAll(d.vmDir(spec.Ref), 0o755); err != nil {
		return nil, err
	}
	warmDM := "fcimg-" + sanitizeTagFC(img.Name) + "-warm"
	devID, err := d.pool.allocate(d.dmName(spec.Ref), func(id int) error {
		return d.pool.snapshotOfQuiesced(img.WarmDevID, id, d.dmName(spec.Ref), warmDM)
	})
	if err != nil {
		return nil, fmt.Errorf("clone rootfs: %w", err)
	}
	m := &vmMeta{Ref: spec.Ref, DevID: devID, NetIdx: devID, Image: sanitizeTagFC(spec.Image), Ephemeral: spec.Ephemeral}
	// Shared deps: attach the family volume when the manager extracted one for
	// this workspace. Best-effort — any failure boots without the drive.
	if deps, err := d.ensureDepsVolume(spec.DepsHostDir); err != nil {
		log.Printf("fc deps volume for %s: %v (booting without)", spec.Ref, err)
	} else {
		m.Deps = deps
	}
	if err := d.saveMeta(m); err != nil {
		return nil, err
	}
	if err := d.prepareGuest(m, spec); err != nil {
		return nil, fmt.Errorf("prepare guest: %w", err)
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
	n0 := &vmNet{Ref: spec.Ref, Idx: m.NetIdx}
	if st, _ := d.Status(ctx, spec.Ref); st == StateRunning {
		return &Instance{Ref: spec.Ref, State: StateRunning, Addr: n0.Addr(d.cfg.GuestPort), StartedAt: time.Now()}, nil
	}
	// Alive but paused (interrupted suspend): resuming is instant and keeps the
	// dev server's state. Booting instead would spawn a second VMM and fail on
	// the tap device the live one still holds.
	if m.PID != 0 && pidAlive(m.PID) {
		if st, err := d.instanceState(spec.Ref); err == nil && st == "Paused" {
			if err := d.api(spec.Ref, "PATCH", "vm", `{"state":"Resumed"}`); err == nil {
				addr := n0.Addr(d.cfg.GuestPort)
				if err := waitHTTP(ctx, addr, 30*time.Second); err == nil {
					return &Instance{Ref: spec.Ref, State: StateRunning, Addr: addr, StartedAt: time.Now()}, nil
				}
			}
		}
		d.kill(m) // unrecoverable: fall through to a fresh boot
	}
	if d.hasSnapshot(spec.Ref) {
		inst, err := d.restore(ctx, m)
		if err == nil {
			return inst, nil
		}
		// a failed restore must not wedge the workload: drop the snapshot
		// and take the fresh-boot path (slow wake beats no wake)
		d.dropSnapshot(spec.Ref)
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
	if m.Ephemeral {
		// Disposable runtime: no 3GB snapshot, no compression — just tear the
		// VMM down. The thin clone stays (CoW, ~free) so the next Start is a
		// fresh boot with the workspace files intact.
		d.kill(m)
		return nil
	}
	if err := d.api(ref, "PATCH", "vm", `{"state":"Paused"}`); err != nil {
		return fmt.Errorf("pause: %w", err)
	}
	body := fmt.Sprintf(`{"snapshot_type":"Full","snapshot_path":%q,"mem_file_path":%q}`,
		d.snapPath(ref, "state"), d.snapPath(ref, "mem"))
	// Writing multiple GB to a busy disk can take minutes; a short timeout here
	// used to abandon the VM mid-suspend. And whatever goes wrong, the VM must
	// not be left paused: a paused guest answers nothing while orchd still
	// believes it is running, so the workload 502s forever with no self-heal
	// (production incident 2026-08-13, 11 workloads stuck).
	if err := d.apiTimeout(ref, "PUT", "snapshot/create", body, 15*time.Minute); err != nil {
		if rerr := d.api(ref, "PATCH", "vm", `{"state":"Resumed"}`); rerr != nil {
			// cannot resume either — kill it so the next request boots fresh
			d.kill(m)
			d.dropSnapshot(ref)
			return fmt.Errorf("snapshot: %w (resume also failed: %v; killed instead)", err, rerr)
		}
		d.dropSnapshot(ref) // a partial snapshot must never be restored
		return fmt.Errorf("snapshot: %w (VM resumed, still serving)", err)
	}
	d.kill(m)
	// Compress off the caller's path: 3GB of mostly-zero pages zstd to
	// ~180MB (measured 17:1), which is what makes snapshotting every
	// suspended project affordable. Wake decompresses (~2-4s). The per-ref
	// lock keeps a racing Start ordered behind the rename.
	go func() { _ = d.Compact(ref) }()
	return nil
}

// Compact compresses a suspended VM's raw memory snapshot in place
// (3GB -> ~180MB measured). Blocking; Suspend runs it on a goroutine, the
// harness and the future reaper call it directly. A missing raw file (already
// compacted, or consumed by a wake) is success; a failed compression keeps
// the raw file so wakes are never blocked.
func (d *FirecrackerDriver) Compact(ref string) error {
	lk := d.lockFor(ref)
	lk.Lock()
	defer lk.Unlock()
	raw := d.snapPath(ref, "mem")
	if _, err := os.Stat(raw); err != nil {
		return nil
	}
	if _, err := exec.LookPath("zstd"); err != nil {
		return fmt.Errorf("zstd not installed; raw snapshot kept")
	}
	if err := sh("zstd", "-3", "-T4", "-q", "-f", "--rm", raw, "-o", raw+".zst"); err != nil {
		_ = os.Remove(raw + ".zst")
		return err
	}
	return nil
}

// Stop halts without a snapshot; the next Start is a fresh boot.
func (d *FirecrackerDriver) Stop(ctx context.Context, ref string) error {
	m, err := d.meta(ref)
	if err != nil {
		return nil
	}
	d.kill(m)
	d.dropSnapshot(ref)
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
		// Alive is not the same as serving: a VM paused by an interrupted
		// suspend must report as not-running so the manager heals it.
		if st, err := d.instanceState(ref); err == nil && st == "Paused" {
			return StateSuspended, nil
		}
		return StateRunning, nil
	}
	if d.hasSnapshot(ref) {
		return StateSuspended, nil
	}
	if m.Ephemeral {
		// clone-at-rest with no snapshot: still resumable via fresh boot
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
// makeRoom keeps the number of running VMs under the cap by stopping the
// longest-running ones. Stopping is a kill, not a snapshot: it frees memory
// immediately (a snapshot under memory pressure is exactly what fails), and
// the workload boots again on its next request. Boot age is a proxy for
// idleness — the manager's reaper handles genuinely idle workloads properly;
// this is the backstop that keeps the host from thrashing.
// countLive counts VMs with a live VMM process, excluding one ref (the VM
// being spawned may already have stale state on disk).
func (d *FirecrackerDriver) countLive(exclude string) int {
	entries, err := os.ReadDir(filepath.Join(d.cfg.Root, "vms"))
	if err != nil {
		return 0
	}
	n := 0
	for _, e := range entries {
		if e.Name() == exclude {
			continue
		}
		// Read the meta file directly — d.meta() takes d.mu, and this count
		// runs under admitMu from code paths that may already hold d.mu.
		b, err := os.ReadFile(filepath.Join(d.cfg.Root, "vms", e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		m := &vmMeta{}
		if json.Unmarshal(b, m) == nil && m.PID != 0 && pidIsFirecracker(m.PID) {
			n++
		}
	}
	return n
}

// pidIsFirecracker reports whether pid is a live firecracker process. A bare
// liveness check is not enough for capacity accounting: metas left by killed
// VMs record PIDs the kernel reuses, and each reused PID counts as a phantom
// VM. Enough phantoms push the count past MaxLive permanently — every spawn
// then waits its full admission deadline while holding its workload lock, and
// the gateway wedges for every host behind those locks. (Live incident,
// 2026-08-16 evening: load 0.6, gateway timing out for everything.)
func pidIsFirecracker(pid int) bool {
	b, err := os.ReadFile(fmt.Sprintf("/proc/%d/comm", pid))
	if err != nil {
		return false
	}
	return strings.TrimSpace(string(b)) == "firecracker"
}

// admit blocks until there is capacity for one more VM, then reserves it via
// pendingSpawns (released by the caller once the PID is recorded, or on spawn
// failure). It deliberately never evicts: the previous behaviour killed the
// oldest VM to admit the newest, which under a provision burst became a
// kill/boot churn loop — every admission destroyed a VM that was itself still
// bundling, the work restarted on its next request, and the box melted doing
// nothing twice. Waiting is honest: the reaper and finished workloads free
// capacity, and a caller that cannot be admitted in time fails loudly.
func (d *FirecrackerDriver) admit(exclude string) error {
	if d.cfg.MaxLive <= 0 {
		return nil
	}
	deadline := time.Now().Add(10 * time.Minute)
	logged := false
	for {
		d.admitMu.Lock()
		live := d.countLive(exclude) + d.pendingSpawns
		if live < d.cfg.MaxLive {
			d.pendingSpawns++
			d.admitMu.Unlock()
			return nil
		}
		d.admitMu.Unlock()
		if !logged {
			log.Printf("fc: at capacity (%d/%d live) — %s waiting for a slot", live, d.cfg.MaxLive, exclude)
			logged = true
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("at capacity: %d/%d VMs live for 10m — refusing to overcommit host memory", live, d.cfg.MaxLive)
		}
		time.Sleep(2 * time.Second)
	}
}

func (d *FirecrackerDriver) admitDone() {
	d.admitMu.Lock()
	if d.pendingSpawns > 0 {
		d.pendingSpawns--
	}
	d.admitMu.Unlock()
}

func (d *FirecrackerDriver) spawn(m *vmMeta) error {
	if err := d.admit(m.Ref); err != nil {
		return err
	}
	admitted := true
	defer func() {
		if admitted {
			d.admitDone()
		}
	}()
	// The rootfs mapping can be missing (reboot, operator cleanup); recreate it
	// from the recorded device id rather than failing the boot.
	if _, err := os.Stat("/dev/mapper/" + d.dmName(m.Ref)); err != nil {
		if perr := d.pool.ensurePool(); perr == nil {
			_ = d.pool.activate(m.DevID, d.dmName(m.Ref))
		}
	}
	dir := d.vmDir(m.Ref)
	_ = os.Remove(d.sockPath(m.Ref))
	if err := os.Symlink("/dev/mapper/"+d.dmName(m.Ref), filepath.Join(dir, "rootfs.blk")); err != nil && !os.IsExist(err) {
		return err
	}
	if m.Deps != "" {
		if d.ensureDepsActive(m.Deps) {
			_ = os.Remove(filepath.Join(dir, "deps.blk"))
			if err := os.Symlink("/dev/mapper/"+d.depsDM(m.Deps), filepath.Join(dir, "deps.blk")); err != nil && !os.IsExist(err) {
				return err
			}
		} else {
			// The family volume vanished. A fresh boot survives without it (the
			// baked tree is still in the rootfs), but a snapshot restore would
			// fail on the missing drive — clear the field so future boots are
			// consistent, and let the boot path skip the drive.
			log.Printf("fc deps volume %q missing for %s; booting without", m.Deps, m.Ref)
			m.Deps = ""
			_ = d.saveMeta(m)
		}
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
	// Own session/process group: a signal aimed at orchd (Ctrl-C, systemd
	// stop) must not take tenant VMs with it. Paired with KillMode=process in
	// the unit file.
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		return err
	}
	go cmd.Wait() // reap
	m.PID = cmd.Process.Pid
	d.admitDone()
	admitted = false
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

// ensureGuestBoot prepares the rootfs clone while the VM is down: the resolver
// and any directory the workload's env points at. Host-side on purpose —
// writes made from inside a running guest are lost when the VMM is killed
// without a sync, and baking them into the image would leave every existing
// clone behind.
//
// Runs on every fresh boot, not just at create: nothing in the sandbox hands
// out a resolver (no DHCP — the guest address arrives on the kernel command
// line), so without this a boot-time dependency install fails on DNS; and a
// workload whose TMPDIR does not exist loses its build caches on every start
// ("metro-file-map Cache write error: ENOENT" — cosmetic but it makes every
// cold start slower).
func (d *FirecrackerDriver) ensureGuestBoot(m *vmMeta, spec Spec) {
	mnt := filepath.Join(d.vmDir(m.Ref), "mnt")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return
	}
	if err := sh("mount", "/dev/mapper/"+d.dmName(m.Ref), mnt); err != nil {
		return // best-effort: a guest that is already set up keeps working
	}
	defer sh("umount", mnt)
	_ = os.WriteFile(filepath.Join(mnt, "etc", "resolv.conf"), []byte(fcResolvConf), 0o644)
	// Absolute env paths the workload expects to exist. Only paths inside the
	// guest's own writable areas, and only from env the caller set.
	for _, key := range []string{"TMPDIR", "HOME", "METRO_CACHE_DIR", "XDG_CACHE_HOME"} {
		p := spec.Env[key]
		if p == "" || !strings.HasPrefix(p, "/") || strings.Contains(p, "..") {
			continue
		}
		if !strings.HasPrefix(p, "/data") && !strings.HasPrefix(p, "/tmp") && !strings.HasPrefix(p, "/cache") {
			continue
		}
		if err := os.MkdirAll(filepath.Join(mnt, strings.TrimPrefix(p, "/")), 0o1777); err != nil {
			log.Printf("fc %s: could not create %s in guest: %v", m.Ref, p, err)
		}
	}
}

func (d *FirecrackerDriver) boot(ctx context.Context, m *vmMeta, spec Spec, _ bool) (*Instance, error) {
	d.ensureGuestBoot(m, spec)
	select {
	case d.bootSem <- struct{}{}:
		defer func() { <-d.bootSem }()
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	if err := d.spawn(m); err != nil {
		return nil, err
	}
	// Dev-server workloads walk very large dependency trees, and a watcher that
	// runs out of inotify watches takes the whole server down mid-startup
	// ("ENOSPC: System limit for number of file watchers reached" — observed on
	// a project whose tree includes ~420k files). The guest kernel's defaults
	// are far too low for that, and sysctl.* boot parameters let us raise them
	// without baking anything into the image.
	bootArgs := fmt.Sprintf(
		"console=ttyS0 reboot=k panic=1 pci=off init=/sbin/orchd-init ip=%s::%s:255.255.255.240::eth0:off"+
			" sysctl.fs.inotify.max_user_watches=1048576 sysctl.fs.inotify.max_user_instances=8192"+
			" sysctl.fs.file-max=1048576",
		fcGuestIP, fcTapIP)
	steps := []struct{ path, body string }{
		{"boot-source", fmt.Sprintf(`{"kernel_image_path":%q,"boot_args":%q}`, d.cfg.Kernel, bootArgs)},
		{"drives/rootfs", `{"drive_id":"rootfs","path_on_host":"rootfs.blk","is_root_device":true,"is_read_only":false}`},
	}
	if m.Deps != "" {
		steps = append(steps, struct{ path, body string }{"drives/deps", depsDriveBody()})
	}
	steps = append(steps, []struct{ path, body string }{
		{"network-interfaces/eth0", fmt.Sprintf(`{"iface_id":"eth0","guest_mac":"06:00:AC:10:00:02","host_dev_name":%q}`, fcTapName)},
		{"machine-config", fmt.Sprintf(`{"vcpu_count":%d,"mem_size_mib":%d}`, d.cfg.VcpuCount, d.cfg.MemSizeMiB)},
		{"actions", `{"action_type":"InstanceStart"}`},
	}...)
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

func (d *FirecrackerDriver) hasSnapshot(ref string) bool {
	if _, err := os.Stat(d.snapPath(ref, "mem")); err == nil {
		return true
	}
	_, err := os.Stat(d.snapPath(ref, "mem") + ".zst")
	return err == nil
}

func (d *FirecrackerDriver) dropSnapshot(ref string) {
	_ = os.Remove(d.snapPath(ref, "mem"))
	_ = os.Remove(d.snapPath(ref, "mem") + ".zst")
	_ = os.Remove(d.snapPath(ref, "state"))
}

func (d *FirecrackerDriver) restore(ctx context.Context, m *vmMeta) (*Instance, error) {
	// Serialize against an in-flight post-suspend compression, then
	// materialize the raw mem file if only the compressed form remains.
	lk := d.lockFor(m.Ref)
	lk.Lock()
	raw := d.snapPath(m.Ref, "mem")
	if _, err := os.Stat(raw); err != nil {
		if err := sh("zstd", "-d", "-T4", "-q", "-f", "--rm", raw+".zst", "-o", raw); err != nil {
			lk.Unlock()
			return nil, fmt.Errorf("decompress snapshot: %w", err)
		}
	}
	lk.Unlock()
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
	d.dropSnapshot(m.Ref)
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
	return d.apiTimeout(ref, method, path, body, 60*time.Second)
}

// instanceState reports the VMM's own view of itself ("Running", "Paused",
// "Not started"). Cheap, and the only way to tell a serving VM from one that
// was paused and never resumed.
func (d *FirecrackerDriver) instanceState(ref string) (string, error) {
	// DisableKeepAlives: each call builds a throwaway Transport, and a kept-alive
	// connection in a discarded transport is never closed. Status is polled on
	// every wake and every reaper pass, so the leaked sockets accumulated until
	// Firecracker's API server answered 503 "Too many open connections" — at
	// which point the VM could no longer be paused, silently defeating
	// scale-to-zero for it.
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", d.sockPath(ref))
			},
			DisableKeepAlives: true,
		},
		Timeout: 5 * time.Second,
	}
	res, err := client.Get("http://fc/")
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	var info struct {
		State string `json:"state"`
	}
	if err := json.NewDecoder(res.Body).Decode(&info); err != nil {
		return "", err
	}
	return info.State, nil
}

func (d *FirecrackerDriver) apiTimeout(ref, method, path, body string, timeout time.Duration) error {
	client := http.Client{
		Transport: &http.Transport{
			DialContext: func(ctx context.Context, _, _ string) (net.Conn, error) {
				return net.Dial("unix", d.sockPath(ref))
			},
			// See instanceState: throwaway transports must not keep connections alive.
			DisableKeepAlives: true,
		},
		Timeout: timeout,
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

// prepareGuest mounts the VM's rootfs clone while it is down and (a) drops
// the workload env where the baked init sources it, (b) syncs the project's
// materialized workspace over the image's /app — the guest boots the image
// scaffold otherwise and would never see the project's delta files.
// node_modules is excluded: the image's installed tree stays authoritative
// (the boot wrapper's pkg-hash guard installs additions).
func (d *FirecrackerDriver) prepareGuest(m *vmMeta, spec Spec) error {
	mnt := filepath.Join(d.vmDir(m.Ref), "mnt")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return err
	}
	if err := sh("mount", "/dev/mapper/"+d.dmName(m.Ref), mnt); err != nil {
		return err
	}
	defer sh("umount", mnt)
	var b strings.Builder
	for k, v := range spec.Env {
		b.WriteString(fmt.Sprintf("export %s=%q\n", k, v))
	}
	if err := os.WriteFile(filepath.Join(mnt, "etc", "orchd.env"), []byte(b.String()), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(mnt, "etc", "resolv.conf"), []byte(fcResolvConf), 0o644); err != nil {
		return err
	}
	src := spec.DataDir
	if spec.WorkspaceDir != "" {
		src = filepath.Join(spec.DataDir, spec.WorkspaceDir)
	}
	if st, err := os.Stat(src); err == nil && st.IsDir() {
		if err := sh("rsync", "-a", "--delete-after", "--exclude", "node_modules", "--exclude", ".expo",
			src+"/", filepath.Join(mnt, "app")+"/"); err != nil {
			return fmt.Errorf("workspace sync: %w", err)
		}
	}
	return sh("sync")
}

// DeleteFileInGuest removes a file inside the running guest via the agent.
func (d *FirecrackerDriver) DeleteFileInGuest(ctx context.Context, ref, path string) error {
	m, err := d.meta(ref)
	if err != nil {
		return err
	}
	n := &vmNet{Ref: ref, Idx: m.NetIdx}
	body, _ := json.Marshal(map[string]string{"p": path})
	req, _ := http.NewRequestWithContext(ctx, "DELETE", fmt.Sprintf("http://%s/file", n.Addr(d.cfg.AgentPort)), bytes.NewReader(body))
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		return err
	}
	res.Body.Close()
	return nil
}

func pidAlive(pid int) bool {
	err := sh("kill", "-0", strconv.Itoa(pid))
	return err == nil
}
