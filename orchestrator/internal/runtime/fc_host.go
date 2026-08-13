//go:build linux

package runtime

import (
	"fmt"
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

func (p *thinPool) nextDevID() (int, error) {
	f := filepath.Join(p.root, "next-dev-id")
	b, _ := os.ReadFile(f)
	n, _ := strconv.Atoi(strings.TrimSpace(string(b)))
	if n < 100 {
		n = 100 // ids below 100 are reserved for image base/warm volumes
	}
	if err := os.WriteFile(f, []byte(strconv.Itoa(n+1)), 0o644); err != nil {
		return 0, err
	}
	return n, nil
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
// The origin device must be quiesced (suspended or not actively written).
func (p *thinPool) snapshotOf(originID, devID int, dmName string) error {
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
