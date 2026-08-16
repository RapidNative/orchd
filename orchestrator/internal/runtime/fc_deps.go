//go:build linux

package runtime

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// Shared node_modules volumes for microVMs.
//
// The docker driver shares an image's extracted dependencies across containers
// with an overlay mount (prepareDepsOverlay); microVMs had nothing, so every
// clone whose manifest diverged from the baked image rewrote node_modules into
// its own rootfs — package managers touch files even when versions match, and
// every touched file breaks copy-on-write block sharing. On a fleet of
// converted projects that meant paying nearly a full node_modules of pool
// blocks per VM, and losing it all on recreate.
//
// The same shared tree, as a block device: one read-only ext4 volume per
// dependency family (the lockfile hash the manager already computes for the
// docker path), attached to every VM in the family as a second drive. The
// guest overlays it under /app/node_modules with a writable upper on /data, so
// boot installs write only their true delta. A read-only device attached to
// many VMs concurrently is safe, and because the drive is identical for every
// family member, memory snapshots stay portable.
//
// Strictly additive: no family volume (or no DepsHostDir on the spec) means
// the VM boots exactly as before. Family volumes are never deleted by VM
// teardown — they are shared, and a memory snapshot taken with the drive
// attached needs the volume present to restore.

// depsName derives the family volume name from the manager's shared deps dir
// (…/shared-deps/<lockhash>-<workspace>), which is content-addressed already.
func depsName(depsHostDir string) string {
	return sanitizeTagFC(filepath.Base(depsHostDir))
}

func (d *FirecrackerDriver) depsDir(name string) string {
	return filepath.Join(d.cfg.Root, "deps", name)
}

func (d *FirecrackerDriver) depsDM(name string) string { return "fcdeps-" + name }

type fcDepsMeta struct {
	Name  string `json:"name"`
	DevID int    `json:"dev_id"`
}

func (d *FirecrackerDriver) depsMeta(name string) (*fcDepsMeta, error) {
	b, err := os.ReadFile(filepath.Join(d.depsDir(name), "meta.json"))
	if err != nil {
		return nil, err
	}
	m := &fcDepsMeta{}
	return m, json.Unmarshal(b, m)
}

// ensureDepsVolume returns the family volume for srcDir, building it on first
// use: a thin volume with an ext4 filesystem holding srcDir's tree under
// /node_modules. Best-effort by contract — a nil error with an empty name
// means "boot without the drive", never a failed create.
func (d *FirecrackerDriver) ensureDepsVolume(srcDir string) (string, error) {
	if srcDir == "" {
		return "", nil
	}
	if _, err := os.Stat(srcDir); err != nil {
		return "", nil // manager promised deps that aren't there; boot without
	}
	name := depsName(srcDir)

	// One builder per family; losers of the race find the meta and reuse it.
	lk := d.lockFor("deps:" + name)
	lk.Lock()
	defer lk.Unlock()

	if m, err := d.depsMeta(name); err == nil {
		// Present. Reactivate the mapping if a reboot dropped it.
		if _, err := os.Stat("/dev/mapper/" + d.depsDM(name)); err != nil {
			if perr := d.pool.ensurePool(); perr != nil {
				return "", nil
			}
			if err := d.pool.activate(m.DevID, d.depsDM(name)); err != nil {
				return "", nil
			}
		}
		return name, nil
	}

	if err := os.MkdirAll(d.depsDir(name), 0o755); err != nil {
		return "", err
	}
	devID, err := d.pool.allocate(d.depsDM(name), func(id int) error {
		return d.pool.createThin(id, d.depsDM(name))
	})
	if err != nil {
		return "", fmt.Errorf("deps volume %s: %w", name, err)
	}
	dev := "/dev/mapper/" + d.depsDM(name)
	if err := sh("mkfs.ext4", "-q", dev); err != nil {
		return "", fmt.Errorf("mkfs deps %s: %w", name, err)
	}
	mnt := filepath.Join(d.depsDir(name), "mnt")
	if err := os.MkdirAll(mnt, 0o755); err != nil {
		return "", err
	}
	if err := sh("mount", dev, mnt); err != nil {
		return "", fmt.Errorf("mount deps %s: %w", name, err)
	}
	defer func() { _ = sh("umount", mnt) }()
	if err := os.MkdirAll(filepath.Join(mnt, "node_modules"), 0o755); err != nil {
		return "", err
	}
	// -a: preserve symlinks/permissions; trailing /. copies contents.
	if err := sh("cp", "-a", srcDir+"/.", filepath.Join(mnt, "node_modules")); err != nil {
		return "", fmt.Errorf("populate deps %s: %w", name, err)
	}
	if err := sh("sync"); err != nil {
		return "", err
	}
	meta, _ := json.Marshal(&fcDepsMeta{Name: name, DevID: devID})
	if err := os.WriteFile(filepath.Join(d.depsDir(name), "meta.json"), meta, 0o644); err != nil {
		return "", err
	}
	return name, nil
}

// ensureDepsActive re-activates a family volume's dm mapping (reboot, operator
// cleanup) so spawn can symlink it. Missing meta means the volume is gone;
// the caller boots without the drive rather than failing the VM.
func (d *FirecrackerDriver) ensureDepsActive(name string) bool {
	if name == "" {
		return false
	}
	if _, err := os.Stat("/dev/mapper/" + d.depsDM(name)); err == nil {
		return true
	}
	m, err := d.depsMeta(name)
	if err != nil {
		return false
	}
	if err := d.pool.ensurePool(); err != nil {
		return false
	}
	return d.pool.activate(m.DevID, d.depsDM(name)) == nil
}

// depsDriveBody is the Firecracker drive config for the family volume: a
// non-root, read-only second disk the guest sees as /dev/vdb.
func depsDriveBody() string {
	return `{"drive_id":"deps","path_on_host":"deps.blk","is_root_device":false,"is_read_only":true}`
}

// depsInUse reports the family volumes referenced by any VM meta, for GC
// tooling: a family volume may only be removed when no VM (running or
// suspended — snapshots restore with the drive) references it.
func (d *FirecrackerDriver) depsInUse() map[string]bool {
	used := map[string]bool{}
	entries, err := os.ReadDir(filepath.Join(d.cfg.Root, "vms"))
	if err != nil {
		return used
	}
	for _, e := range entries {
		b, err := os.ReadFile(filepath.Join(d.cfg.Root, "vms", e.Name(), "meta.json"))
		if err != nil {
			continue
		}
		m := &vmMeta{}
		if json.Unmarshal(b, m) == nil && strings.TrimSpace(m.Deps) != "" {
			used[m.Deps] = true
		}
	}
	return used
}
