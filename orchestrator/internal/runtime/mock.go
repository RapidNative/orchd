package runtime

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"
)

// MockDriver is an in-memory runtime that launches nothing. It implements both
// Runtime and ImageManager so the full control plane (including image
// management) can run without Docker — used for local dev and browser E2E where
// mocking Docker is exactly the point. It is safe for concurrent use.
type MockDriver struct {
	mu        sync.Mutex
	images    []ImageInfo
	instances map[string]State
}

// NewMockDriver returns a mock driver seeded with a couple of images so the
// Images view has content out of the box.
func NewMockDriver() *MockDriver {
	return &MockDriver{
		instances: map[string]State{},
		images: []ImageInfo{
			{
				Repository: "tinbase", Tag: "0.10.0", Ref: "tinbase:0.10.0",
				ID: "c6e4e9b7f1bd", Digest: "sha256:c6e4e9b7f1bdd19a92f6900f2b3c4d5e6f708192a3b4c5d6e7f8091a2b3c4d5e",
				Size: "668MB", CreatedAt: "3 days ago",
			},
			{
				Repository: "rn-vite", Tag: "dev", Ref: "rn-vite:dev",
				ID: "0811f26fd717", Digest: "sha256:0811f26fd717b816aa5d9c0b1a2c3d4e5f60718293a4b5c6d7e8f90a1b2c3d4e5",
				Size: "546MB", CreatedAt: "3 days ago",
			},
		},
	}
}

func (d *MockDriver) Name() string { return "mock" }

func (d *MockDriver) Stats(context.Context, string) (Stats, error) {
	return Stats{MemUsage: "64MiB / 384MiB", MemPerc: "16.7%", CPUPerc: "0.5%"}, nil
}

func (d *MockDriver) Logs(_ context.Context, ref string, _ int) (string, error) {
	return "mock logs for " + ref + "\nready.\n", nil
}

func (d *MockDriver) Create(_ context.Context, s Spec) (*Instance, error) {
	d.mu.Lock()
	d.instances[s.Ref] = StateRunning
	d.mu.Unlock()
	return &Instance{Ref: s.Ref, State: StateRunning, Addr: "127.0.0.1:1", StartedAt: time.Now()}, nil
}

func (d *MockDriver) Start(_ context.Context, s Spec) (*Instance, error) {
	return d.Create(context.Background(), s)
}

func (d *MockDriver) Suspend(_ context.Context, ref string) error {
	d.mu.Lock()
	d.instances[ref] = StateSuspended
	d.mu.Unlock()
	return nil
}

func (d *MockDriver) Stop(_ context.Context, ref string) error {
	d.mu.Lock()
	d.instances[ref] = StateStopped
	d.mu.Unlock()
	return nil
}

func (d *MockDriver) Status(_ context.Context, ref string) (State, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	if st, ok := d.instances[ref]; ok {
		return st, nil
	}
	return StateStopped, nil
}

// ---- ImageManager ----

func (d *MockDriver) ListImages(context.Context, string) ([]ImageInfo, error) {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]ImageInfo, len(d.images))
	copy(out, d.images)
	return out, nil
}

func (d *MockDriver) PullImage(_ context.Context, _, ref string) (string, error) {
	repo, tag := splitRef(ref)
	d.mu.Lock()
	defer d.mu.Unlock()
	full := repo + ":" + tag
	for _, im := range d.images {
		if im.Ref == full {
			return "Status: Image is up to date for " + full + "\n", nil
		}
	}
	d.images = append(d.images, ImageInfo{
		Repository: repo, Tag: tag, Ref: full,
		ID:     "mock" + fmt.Sprintf("%08x", len(d.images)+1),
		Digest: "sha256:mock" + fmt.Sprintf("%060x", len(d.images)+1),
		Size:   "128MB", CreatedAt: "just now",
	})
	return "Status: Downloaded newer image for " + full + "\n", nil
}

func (d *MockDriver) RemoveImage(_ context.Context, _, ref string, _ bool) error {
	d.mu.Lock()
	defer d.mu.Unlock()
	for i, im := range d.images {
		if im.Ref == ref || im.ID == ref || strings.HasPrefix(im.Digest, ref) {
			d.images = append(d.images[:i], d.images[i+1:]...)
			return nil
		}
	}
	return fmt.Errorf("No such image: %s", ref)
}

// splitRef splits "repo:tag" into its parts, defaulting the tag to "latest".
// A digest ("repo@sha256:…") keeps the digest as the tag.
func splitRef(ref string) (repo, tag string) {
	if at := strings.IndexByte(ref, '@'); at >= 0 {
		return ref[:at], ref[at+1:]
	}
	// Only treat the last colon as a tag separator if it's after any slash (so a
	// registry host:port in the ref isn't mistaken for a tag).
	if i := strings.LastIndexByte(ref, ':'); i > strings.LastIndexByte(ref, '/') {
		return ref[:i], ref[i+1:]
	}
	return ref, "latest"
}
