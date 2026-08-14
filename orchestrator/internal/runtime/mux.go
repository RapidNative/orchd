//go:build linux

package runtime

import (
	"context"
	"fmt"
	"log"
	"strings"
)

// Mux routes workloads between the default driver (docker/gVisor) and the
// FirecrackerDriver: Create consults a template/workspace allowlist; every
// ref-keyed call after that routes by ownership (the fc driver knows its
// refs durably via vms/<ref>/meta.json). This keeps the manager single-
// runtime in shape — m.rt stays one value — while a pilot template runs on
// microVMs and everything else is untouched.
type Mux struct {
	def Runtime
	fc  *FirecrackerDriver
	// allow holds "template/workspace" keys, e.g. "fullstack-supabase/mobile".
	allow map[string]bool
}

func NewMux(def Runtime, fc *FirecrackerDriver, allowList string) *Mux {
	allow := map[string]bool{}
	for _, k := range strings.Split(allowList, ",") {
		if k = strings.TrimSpace(k); k != "" {
			allow[k] = true
		}
	}
	return &Mux{def: def, fc: fc, allow: allow}
}

func (m *Mux) picksFC(spec Spec) bool {
	return m.allow[spec.Template+"/"+spec.Workspace]
}

func (m *Mux) forRef(ref string) Runtime {
	if m.fc.Knows(ref) {
		return m.fc
	}
	return m.def
}

func (m *Mux) Name() string { return m.def.Name() + "+firecracker" }

func (m *Mux) Create(ctx context.Context, spec Spec) (*Instance, error) {
	if m.picksFC(spec) {
		inst, err := m.fc.Create(ctx, spec)
		if err == nil {
			return inst, nil
		}
		// No silent downgrade. Falling back to the default runtime kept
		// creation working but hid the real fault and gave the workload the
		// slow path for its whole life — observed as "why is this project on
		// docker?" with the actual cause (no guest DNS, so its dependency
		// install died) buried in the journal. An allowlisted workload runs as
		// a microVM or it fails loudly; the half-made VM is cleaned up so a
		// retry starts fresh.
		log.Printf("fc create %s failed: %v", spec.Ref, err)
		_ = m.fc.Delete(ctx, spec.Ref)
		return nil, fmt.Errorf("firecracker create %s: %w", spec.Ref, err)
	}
	return m.def.Create(ctx, spec)
}

func (m *Mux) Start(ctx context.Context, spec Spec) (*Instance, error) {
	// Strict ownership: only refs the fc driver already owns wake through it.
	// Routing Start by the allowlist re-CREATED a shadow microVM for refs
	// whose fc create had fallen back to docker — two instances fighting over
	// one ref, the gateway pointing at the dead one (observed 2026-08-13).
	// Runtime choice happens exactly once, at Create.
	if m.fc.Knows(spec.Ref) {
		return m.fc.Start(ctx, spec)
	}
	return m.def.Start(ctx, spec)
}

func (m *Mux) Suspend(ctx context.Context, ref string) error { return m.forRef(ref).Suspend(ctx, ref) }
func (m *Mux) Stop(ctx context.Context, ref string) error    { return m.forRef(ref).Stop(ctx, ref) }
func (m *Mux) Status(ctx context.Context, ref string) (State, error) {
	return m.forRef(ref).Status(ctx, ref)
}
func (m *Mux) Stats(ctx context.Context, ref string) (Stats, error) {
	return m.forRef(ref).Stats(ctx, ref)
}
func (m *Mux) Logs(ctx context.Context, ref string, tail int) (string, error) {
	return m.forRef(ref).Logs(ctx, ref, tail)
}

// ---- optional capabilities the manager asserts on m.rt ----

// WriteFileInContainer / DeleteFileInContainer: the manager's write-through.
// FC refs go through the guest agent; others forward to the default driver
// when it supports the capability.
func (m *Mux) WriteFileInContainer(ctx context.Context, ref, path string, content []byte) error {
	if m.fc.Knows(ref) {
		return m.fc.WriteFile(ctx, ref, path, content)
	}
	if cw, ok := m.def.(interface {
		WriteFileInContainer(context.Context, string, string, []byte) error
	}); ok {
		return cw.WriteFileInContainer(ctx, ref, path, content)
	}
	return nil
}

func (m *Mux) DeleteFileInContainer(ctx context.Context, ref, path string) error {
	if m.fc.Knows(ref) {
		return m.fc.DeleteFileInGuest(ctx, ref, path)
	}
	if cd, ok := m.def.(interface {
		DeleteFileInContainer(context.Context, string, string) error
	}); ok {
		return cd.DeleteFileInContainer(ctx, ref, path)
	}
	return nil
}

// RemoveVolumes: workload deletion. FC's equivalent releases the microVM's
// thin volume, namespace and snapshots.
func (m *Mux) RemoveVolumes(ctx context.Context, ref string) error {
	if m.fc.Knows(ref) {
		return m.fc.Delete(ctx, ref)
	}
	if vr, ok := m.def.(interface {
		RemoveVolumes(context.Context, string) error
	}); ok {
		return vr.RemoveVolumes(ctx, ref)
	}
	return nil
}

// ImageManager passthrough — container images stay a default-driver concern.
func (m *Mux) ListImages(ctx context.Context, host string) ([]ImageInfo, error) {
	if im, ok := m.def.(ImageManager); ok {
		return im.ListImages(ctx, host)
	}
	return nil, errUnsupported
}
func (m *Mux) PullImage(ctx context.Context, host, ref string) (string, error) {
	if im, ok := m.def.(ImageManager); ok {
		return im.PullImage(ctx, host, ref)
	}
	return "", errUnsupported
}
func (m *Mux) RemoveImage(ctx context.Context, host, ref string, force bool) error {
	if im, ok := m.def.(ImageManager); ok {
		return im.RemoveImage(ctx, host, ref, force)
	}
	return errUnsupported
}

// FC exposes the wrapped firecracker driver (harness / tests).
func (m *Mux) FC() *FirecrackerDriver { return m.fc }

// KnowsMicroVM reports fc ownership of a ref — the manager's wake-on-write
// asserts this (as an anonymous interface, keeping manager buildable on
// platforms where the fc driver doesn't exist).
func (m *Mux) KnowsMicroVM(ref string) bool { return m.fc.Knows(ref) }

// PicksFC reports whether a template/workspace pair is on the microVM
// allowlist — the manager's image-build hook uses it to prepare FC images.
func (m *Mux) PicksFC(tmpl, workspace string) bool { return m.allow[tmpl+"/"+workspace] }

// PrepareFCImage is the manager-facing image-prep entry (anonymous-interface
// asserted, same reason as KnowsMicroVM).
func (m *Mux) PrepareFCImage(ctx context.Context, dockerTag, runCmd, warmPath string) error {
	return m.fc.PrepareImage(ctx, dockerTag, runCmd, warmPath)
}
