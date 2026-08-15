//go:build linux

package runtime

import (
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
)

// Host-side plumbing for the firecracker driver: dm-thin volumes and per-VM
// network namespaces. Everything here shells out to the standard tools
// (dmsetup, ip, iptables) — the same operations an operator would run by
// hand, which keeps the driver debuggable on a live box.

// sh runs a command and returns a joined error with its output on failure.
func sh(name string, args ...string) error {
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		return fmt.Errorf("%s %s: %w: %s", name, strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

// ipt adds an iptables rule unless an identical one already exists: the same
// argv is probed with -C first. nsPrefix, when set, runs it inside a network
// namespace ("ip netns exec <ns>").
func ipt(nsPrefix []string, args ...string) error {
	probe := make([]string, len(args))
	copy(probe, args)
	for i, a := range probe {
		if a == "-A" || a == "-I" {
			probe[i] = "-C"
			break
		}
	}
	run := func(a []string) *exec.Cmd {
		full := append(append([]string{}, nsPrefix...), "iptables")
		full = append(full, a...)
		return exec.Command(full[0], full[1:]...)
	}
	if err := run(probe).Run(); err == nil {
		return nil
	}
	out, err := run(args).CombinedOutput()
	if err != nil {
		return fmt.Errorf("iptables %s: %w: %s", strings.Join(args, " "), err, strings.TrimSpace(string(out)))
	}
	return nil
}

func shOut(name string, args ...string) (string, error) {
	out, err := exec.Command(name, args...).Output()
	return strings.TrimSpace(string(out)), err
}

// ---- dm-thin ----

// thinPool wraps a device-mapper thin pool (created out-of-band; see
// `fcharness pool-init`). Volume device ids are allocated from a counter file
// so they survive restarts.
type thinPool struct {
	Name    string // dm name, e.g. "fcpool"
	root    string // driver state root (counter file lives here)
	sectors int64  // per-volume virtual size in 512b sectors
}

func (p *thinPool) dev() string { return "/dev/mapper/" + p.Name }

// nextDevID hands out a thin-volume device id. The counter file is a hint, not
// the truth: the pool's own metadata outlives it (and an operator who removes
// the file, as happened during a cleanup on 2026-08-14, makes the counter walk
// back over ids the pool still holds — every allocation then fails with
// "create_snap: File exists" and the workload never gets a rootfs).
//
// So the floor is whatever the recorded state already uses, and callers retry
// on collision via allocate().
func (p *thinPool) nextDevID() (int, error) {
	f := filepath.Join(p.root, "next-dev-id")
	b, _ := os.ReadFile(f)
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if hw := p.highWaterMark(); hw >= n {
		n = hw + 1
	}
	if n < 100 {
		n = 100 // ids below 100 are reserved for image base/warm volumes
	}
	if err := os.WriteFile(f, []byte(strconv.Itoa(n+1)), 0o644); err != nil {
		return 0, err
	}
	return n, nil
}

// highWaterMark is the largest device id any recorded VM or image is using.
func (p *thinPool) highWaterMark() int {
	max := 0
	consider := func(dir string, pick func(map[string]any) []int) {
		entries, err := os.ReadDir(filepath.Join(p.root, dir))
		if err != nil {
			return
		}
		for _, e := range entries {
			b, err := os.ReadFile(filepath.Join(p.root, dir, e.Name(), "meta.json"))
			if err != nil {
				continue
			}
			var m map[string]any
			if json.Unmarshal(b, &m) != nil {
				continue
			}
			for _, id := range pick(m) {
				if id > max {
					max = id
				}
			}
		}
	}
	num := func(m map[string]any, k string) int {
		if v, ok := m[k].(float64); ok {
			return int(v)
		}
		return 0
	}
	consider("vms", func(m map[string]any) []int { return []int{num(m, "dev_id")} })
	consider("images", func(m map[string]any) []int { return []int{num(m, "base_dev_id"), num(m, "warm_dev_id")} })
	return max
}

// allocate runs fn with fresh device ids until the pool accepts one. A
// collision means the pool already holds that id (stale metadata, a counter
// reset, a volume whose record we lost) — taking the next id is both correct
// and cheap; ids are 24-bit.
func (p *thinPool) allocate(dmName string, fn func(devID int) error) (int, error) {
	var lastErr error
	for attempt := 0; attempt < 32; attempt++ {
		devID, err := p.nextDevID()
		if err != nil {
			return 0, err
		}
		if err := fn(devID); err == nil {
			return devID, nil
		} else if !strings.Contains(err.Error(), "File exists") {
			return 0, err
		} else {
			lastErr = err
			_ = sh("dmsetup", "remove", dmName) // drop any half-made mapping
		}
	}
	return 0, fmt.Errorf("no free thin device id after 32 attempts: %w", lastErr)
}

// createThin makes a brand-new empty thin volume with the given device id and
// activates it under dmName.
func (p *thinPool) createThin(devID int, dmName string) error {
	if err := sh("dmsetup", "message", p.dev(), "0", fmt.Sprintf("create_thin %d", devID)); err != nil {
		return err
	}
	return p.activate(devID, dmName)
}

// snapshotOf creates a CoW snapshot of origin (by device id) and activates it.
//
// device-mapper requires the origin to be SUSPENDED across create_snap: that is
// what flushes its in-flight IO into the pool. Skipping it yields a snapshot of
// whatever happened to be committed — in the worst case an empty device with no
// filesystem, which then fails to mount ("can't read superblock") or boots into
// a kernel panic. It went unnoticed while origins were idle, and appeared the
// moment an image was snapshotted right after being written.
//
// originDM is the origin's device-mapper name; when empty the origin is assumed
// already quiesced.
func (p *thinPool) snapshotOf(originID, devID int, dmName string) error {
	return p.snapshotOfQuiesced(originID, devID, dmName, "")
}

func (p *thinPool) snapshotOfQuiesced(originID, devID int, dmName, originDM string) error {
	if originDM != "" {
		if _, err := os.Stat("/dev/mapper/" + originDM); err == nil {
			if err := sh("dmsetup", "suspend", originDM); err != nil {
				return fmt.Errorf("suspend origin %s: %w", originDM, err)
			}
			defer sh("dmsetup", "resume", originDM)
		}
	}
	if err := sh("dmsetup", "message", p.dev(), "0", fmt.Sprintf("create_snap %d %d", devID, originID)); err != nil {
		return err
	}
	return p.activate(devID, dmName)
}

func (p *thinPool) activate(devID int, dmName string) error {
	if _, err := os.Stat("/dev/mapper/" + dmName); err == nil {
		return nil
	}
	table := fmt.Sprintf("0 %d thin %s %d", p.sectors, p.dev(), devID)
	return sh("dmsetup", "create", dmName, "--table", table)
}

// ensurePool (re)creates the loopback-backed thin pool if its device-mapper
// table is gone — which is exactly the state after a reboot: the backing files
// persist, the loop attachments and dm tables do not. The pool's METADATA file
// persists too, so recreating the table over the same backing files restores
// every thin device id that was ever allocated; only activation is lost.
//
// A deployment using a real LV instead of loopback files can skip this
// entirely: the LV is already there, and the function no-ops once the pool
// device exists.
func (p *thinPool) ensurePool() error {
	if _, err := os.Stat(p.dev()); err == nil {
		return nil
	}
	data := filepath.Join(p.root, "pool-data.img")
	meta := filepath.Join(p.root, "pool-meta.img")
	st, err := os.Stat(data)
	if err != nil {
		return fmt.Errorf("thin pool backing file missing: %w", err)
	}
	ld, err := ensureLoop(data)
	if err != nil {
		return err
	}
	lm, err := ensureLoop(meta)
	if err != nil {
		return err
	}
	sectors := st.Size() / 512
	table := fmt.Sprintf("0 %d thin-pool %s %s 2048 32768", sectors, lm, ld)
	if err := sh("dmsetup", "create", p.Name, "--table", table); err != nil {
		return fmt.Errorf("recreate thin pool: %w", err)
	}
	log.Printf("firecracker: thin pool %s reactivated (%s over %s/%s)", p.Name, humanSectors(sectors), ld, lm)
	return nil
}

// ensureLoop returns the loop device backing a file, attaching one if needed.
func ensureLoop(file string) (string, error) {
	if out, err := shOut("losetup", "-j", file); err == nil && out != "" {
		if i := strings.Index(out, ":"); i > 0 {
			return out[:i], nil
		}
	}
	dev, err := shOut("losetup", "--find", "--show", file)
	if err != nil {
		return "", fmt.Errorf("losetup %s: %w", file, err)
	}
	return dev, nil
}

func humanSectors(s int64) string { return fmt.Sprintf("%dGiB", s*512/(1024*1024*1024)) }

func (p *thinPool) remove(devID int, dmName string) error {
	_ = sh("dmsetup", "remove", dmName)
	return sh("dmsetup", "message", p.dev(), "0", fmt.Sprintf("delete %d", devID))
}

// ---- per-VM network namespace ----
//
// Every guest is identical inside its sandbox: eth0 = 172.16.0.2/28 behind a
// tap at 172.16.0.1. That uniformity is what makes memory snapshots portable
// across projects. Isolation comes from a network namespace per VM; the host
// reaches the guest through a veth pair with a unique /30, DNAT'd inside the
// namespace to the guest IP.

const (
	fcGuestIP = "172.16.0.2"
	fcTapIP   = "172.16.0.1"
	fcTapName = "tap0"
	// fcVMSubnet covers every VM's host-side veth /30.
	fcVMSubnet = "10.201.0.0/16"
)

type vmNet struct {
	Ref string
	Idx int // unique per VM; picks the veth /30 out of 10.201.0.0/16
}

func (n *vmNet) ns() string { return "fc-" + sanitizeNet(n.Ref) }

func sanitizeNet(s string) string {
	s = strings.ToLower(s)
	if len(s) > 10 {
		s = s[:10]
	}
	return s
}

func (n *vmNet) hostVeth() string { return fmt.Sprintf("vh%d", n.Idx) }
func (n *vmNet) nsIP() string     { return fmt.Sprintf("10.201.%d.%d", (n.Idx*4+2)/256, (n.Idx*4+2)%256) }
func (n *vmNet) hostIP() string   { return fmt.Sprintf("10.201.%d.%d", (n.Idx*4+1)/256, (n.Idx*4+1)%256) }

// Addr is what the gateway dials: the namespace veth IP; every port is
// DNAT'd through to the guest.
func (n *vmNet) Addr(port int) string { return fmt.Sprintf("%s:%d", n.nsIP(), port) }

// ensure builds the namespace, tap, veth and NAT rules. Idempotent.
func (n *vmNet) ensure() error {
	ns := n.ns()
	if _, err := shOut("ip", "netns", "pids", ns); err != nil {
		if err := sh("ip", "netns", "add", ns); err != nil {
			return err
		}
	}
	inNS := func(args ...string) error { return sh("ip", append([]string{"netns", "exec", ns}, args...)...) }
	// tap for the guest
	if err := inNS("ip", "link", "show", fcTapName); err != nil {
		if err := inNS("ip", "tuntap", "add", fcTapName, "mode", "tap"); err != nil {
			return err
		}
		if err := inNS("ip", "addr", "add", fcTapIP+"/28", "dev", fcTapName); err != nil {
			return err
		}
		if err := inNS("ip", "link", "set", fcTapName, "up"); err != nil {
			return err
		}
		_ = inNS("ip", "link", "set", "lo", "up")
	}
	// veth pair host <-> namespace
	if err := sh("ip", "link", "show", n.hostVeth()); err != nil {
		// A re-created ref reuses its namespace but gets a new veth index;
		// the old ve0 inside the ns makes `ip link add ... peer ve0` fail
		// with EEXIST (observed as a create fallback storm). Clear it first.
		_ = inNS("ip", "link", "del", "ve0")
		if err := sh("ip", "link", "add", n.hostVeth(), "type", "veth", "peer", "name", "ve0", "netns", ns); err != nil {
			return err
		}
		if err := sh("ip", "addr", "add", n.hostIP()+"/30", "dev", n.hostVeth()); err != nil {
			return err
		}
		if err := sh("ip", "link", "set", n.hostVeth(), "up"); err != nil {
			return err
		}
		if err := inNS("ip", "addr", "add", n.nsIP()+"/30", "dev", "ve0"); err != nil {
			return err
		}
		if err := inNS("ip", "link", "set", "ve0", "up"); err != nil {
			return err
		}
		// inbound: anything arriving on ve0 goes to the guest; replies
		// masquerade as the tap so the guest needs no host routes.
		if err := inNS("sysctl", "-qw", "net.ipv4.ip_forward=1"); err != nil {
			return err
		}
		if err := inNS("iptables", "-t", "nat", "-A", "PREROUTING", "-i", "ve0", "-j", "DNAT", "--to-destination", fcGuestIP); err != nil {
			return err
		}
		if err := inNS("iptables", "-t", "nat", "-A", "POSTROUTING", "-o", fcTapName, "-j", "MASQUERADE"); err != nil {
			return err
		}
	}
	// Outbound. Applied on every ensure (not just on creation) and idempotent,
	// so namespaces built before this existed gain egress on their next boot.
	//
	// Workloads install their own dependencies at boot when the caller's
	// manifest differs from the image (a package added after the image was
	// built), so a guest with no route to a package registry silently runs
	// with a stale dependency tree — and pays the registry's DNS timeouts
	// first. Containers had this via the docker bridge; microVMs need it
	// spelled out: default route out of the namespace, SNAT to the veth, and
	// SNAT again on the host.
	nsPrefix := []string{"ip", "netns", "exec", ns}
	_ = inNS("ip", "route", "add", "default", "via", n.hostIP(), "dev", "ve0")
	_ = inNS("sysctl", "-qw", "net.ipv4.ip_forward=1")
	if err := ipt(nsPrefix, "-t", "nat", "-A", "POSTROUTING", "-o", "ve0", "-j", "MASQUERADE"); err != nil {
		return fmt.Errorf("guest egress SNAT: %w", err)
	}
	return ensureHostEgress()
}

// ensureHostEgress lets guest subnets reach the outside world: forwarding on,
// and one SNAT rule covering every VM veth. Idempotent and cheap, so it runs
// on each boot rather than needing a bootstrap step.
func ensureHostEgress() error {
	_ = sh("sysctl", "-qw", "net.ipv4.ip_forward=1")
	if err := ipt(nil, "-t", "nat", "-A", "POSTROUTING", "-s", fcVMSubnet, "!", "-d", fcVMSubnet, "-j", "MASQUERADE"); err != nil {
		return fmt.Errorf("host egress SNAT: %w", err)
	}
	// The FORWARD policy is DROP on hosts running docker; allow the veths.
	_ = ipt(nil, "-A", "FORWARD", "-s", fcVMSubnet, "-j", "ACCEPT")
	_ = ipt(nil, "-A", "FORWARD", "-d", fcVMSubnet, "-j", "ACCEPT")
	return nil
}

func (n *vmNet) destroy() {
	_ = sh("ip", "link", "del", n.hostVeth())
	_ = sh("ip", "netns", "del", n.ns())
}
