//go:build linux

// fcharness drives the FirecrackerDriver directly on a box — the S3
// integration harness. It exercises exactly the lifecycle the manager will:
// prepare-image, create, suspend, start (restore), write, logs, delete.
//
//	fcharness pool-init                    # loopback thin pool (idempotent)
//	fcharness prepare  <name> <dockerTag> <runCmd> [warmPath]
//	fcharness create   <name> <ref> [KEY=VAL ...]
//	fcharness suspend  <ref>
//	fcharness start    <name> <ref>       # restore (or fresh boot)
//	fcharness write    <ref> <guestPath> <localFile>
//	fcharness logs     <ref>
//	fcharness status   <ref>
//	fcharness delete   <ref>
package main

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/runtime"
)

// Overridable so the harness can drive a scratch pool (reboot-recovery tests)
// without touching the deployment's state.
var (
	root = envOr("ORCHD_FC_ROOT", "/opt/orchd/data/fc")
	pool = envOr("ORCHD_FC_POOL", "fcorchd")
)

func envOr(k, def string) string {
	if v := os.Getenv(k); v != "" {
		return v
	}
	return def
}

func main() {
	if len(os.Args) < 2 {
		fmt.Println("usage: fcharness <pool-init|prepare|create|suspend|start|write|logs|status|delete> ...")
		os.Exit(2)
	}
	if os.Args[1] == "pool-init" {
		must(poolInit())
		fmt.Println("pool ready:", pool)
		return
	}

	d, err := runtime.NewFirecrackerDriver(runtime.FirecrackerConfig{
		Bin:    envOr("ORCHD_FC_BIN", "/opt/orchd/fc/firecracker"),
		Kernel: envOr("ORCHD_FC_KERNEL", "/opt/orchd/fc/vmlinux"),
		Root:   root,
		Pool:   pool,
	})
	must(err)
	ctx := context.Background()
	t0 := time.Now()

	switch os.Args[1] {
	case "prepare":
		warm := ""
		if len(os.Args) > 5 {
			warm = os.Args[5]
		}
		_ = os.Args[2] // legacy name arg kept for CLI compatibility; name derives from the tag
		must(d.PrepareImage(ctx, os.Args[3], os.Args[4], warm))
		fmt.Println("image prepared in", time.Since(t0).Round(time.Millisecond))
	case "create":
		env := map[string]string{}
		eph := false
		for _, kv := range os.Args[4:] {
			if kv == "--ephemeral" {
				eph = true
				continue
			}
			if k, v, ok := strings.Cut(kv, "="); ok {
				env[k] = v
			}
		}
		inst, err := d.Create(ctx, runtime.Spec{Ref: os.Args[3], Image: os.Args[2], Env: env, Ephemeral: eph, ReadyTimeout: 5 * time.Minute})
		must(err)
		fmt.Printf("created %s addr=%s in %s\n", inst.Ref, inst.Addr, time.Since(t0).Round(time.Millisecond))
	case "suspend":
		must(d.Suspend(ctx, os.Args[2]))
		fmt.Println("suspended in", time.Since(t0).Round(time.Millisecond))
		tc := time.Now()
		must(d.Compact(os.Args[2]))
		fmt.Println("compacted in", time.Since(tc).Round(time.Millisecond))
	case "start":
		inst, err := d.Start(ctx, runtime.Spec{Ref: os.Args[3], Image: os.Args[2]})
		must(err)
		fmt.Printf("started %s addr=%s in %s\n", inst.Ref, inst.Addr, time.Since(t0).Round(time.Millisecond))
	case "write":
		b, err := os.ReadFile(os.Args[4])
		must(err)
		must(d.WriteFile(ctx, os.Args[2], os.Args[3], b))
		fmt.Println("written in", time.Since(t0).Round(time.Millisecond))
	case "logs":
		out, err := d.Logs(ctx, os.Args[2], 40)
		must(err)
		fmt.Println(out)
	case "status":
		st, _ := d.Status(ctx, os.Args[2])
		fmt.Println(st)
	case "delete":
		must(d.Delete(ctx, os.Args[2]))
		fmt.Println("deleted")
	default:
		fmt.Println("unknown command", os.Args[1])
		os.Exit(2)
	}
}

// poolInit creates a loopback-backed thin pool for the harness. Production
// will use a real LV (S2's load lesson); loopback is fine for single-VM-at-
// a-time integration runs.
func poolInit() error {
	if err := exec.Command("dmsetup", "info", pool).Run(); err == nil {
		return nil
	}
	if err := os.MkdirAll(root, 0o755); err != nil {
		return err
	}
	run := func(name string, args ...string) error {
		out, err := exec.Command(name, args...).CombinedOutput()
		if err != nil {
			return fmt.Errorf("%s %v: %w: %s", name, args, err, out)
		}
		return nil
	}
	if err := run("truncate", "-s", "60G", root+"/pool-data.img"); err != nil {
		return err
	}
	if err := run("truncate", "-s", "1G", root+"/pool-meta.img"); err != nil {
		return err
	}
	ld, err := exec.Command("losetup", "-f", "--show", root+"/pool-data.img").Output()
	if err != nil {
		return err
	}
	lm, err := exec.Command("losetup", "-f", "--show", root+"/pool-meta.img").Output()
	if err != nil {
		return err
	}
	sectors := int64(60) * 1024 * 1024 * 1024 / 512
	table := fmt.Sprintf("0 %d thin-pool %s %s 2048 32768", sectors,
		strings.TrimSpace(string(lm)), strings.TrimSpace(string(ld)))
	return run("dmsetup", "create", pool, "--table", table)
}

func must(err error) {
	if err != nil {
		fmt.Fprintln(os.Stderr, "FATAL:", err)
		os.Exit(1)
	}
}
