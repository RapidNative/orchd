// Package runtime defines the substrate-agnostic contract for launching tenant
// workloads, plus the generic Workload primitive shared by every workload type
// (tinbase projects today, RapidNative dev environments later).
//
// The whole point of this interface is that the control plane never talks to a
// VMM directly. During local development we drive LocalDriver (plain OS
// processes). On Linux bare metal we will drop in a FirecrackerDriver behind the
// exact same interface, so nothing above this package changes.
package runtime

import (
	"context"
	"time"
)

// WorkloadType is the generic primitive that lets one orchestrator run more than
// one kind of tenant. tinbase-cloud projects and RapidNative dev environments are
// both just workloads with different images and routing.
type WorkloadType string

const (
	WorkloadTinbaseProject WorkloadType = "tinbase-project"
	WorkloadRapidNativeDev WorkloadType = "rapidnative-dev"
)

// State is the lifecycle of a single instance.
type State string

const (
	StateProvisioning State = "provisioning" // first boot / initdb / migrations
	StateRunning      State = "running"      // serving requests
	StateSuspended    State = "suspended"    // scaled to zero, data at rest, resumable
	StateStopped      State = "stopped"      // deliberately halted
	StateFailed       State = "failed"
)

// Spec is everything a driver needs to bring a workload up. It is intentionally
// small; per-workload detail travels in Env so the interface stays stable.
type Spec struct {
	Type WorkloadType
	Ref  string // stable tenant id, also the routing key (<ref>.tinbase.cloud)

	// DataDir is the per-project persistent volume. For LocalDriver it is a host
	// directory; for FirecrackerDriver it will back a virtio-block device. Either
	// way it is decoupled from the instance lifecycle so suspend/resume and
	// scale-to-zero never lose data.
	DataDir string

	// WorkDir is the project working directory (holds supabase/migrations, seed,
	// functions). May be empty; tinbase still boots.
	WorkDir string

	// Image optionally overrides the driver's default container image, so one
	// project can run heterogeneous workloads (a tinbase db, a web runner, an api
	// server). Empty uses the driver default. Ignored by LocalDriver.
	Image string
	// Port optionally overrides the container's listen port. 0 uses the driver
	// default (54321 for tinbase).
	Port int

	// DockerHost places this workload on a specific Docker daemon (a region's
	// worker node), e.g. tcp://node2:2375 or ssh://root@node2. Empty = the driver
	// default (local). The DockerDriver publishes remotely-placed containers on
	// 0.0.0.0 and addresses them by the node host.
	DockerHost string

	// Env is passed to the workload process/VM (JWT secret, engine, secrets).
	Env map[string]string

	// Limits caps the workload's resource use so no single tenant can starve the
	// host. Enforced by the DockerDriver via cgroups; ignored by LocalDriver.
	Limits Limits
}

// Limits are per-workload resource caps.
type Limits struct {
	MemoryMB  int     // hard memory cap (0 = unlimited); OOM-kills the container if exceeded
	CPUs      float64 // fractional CPU cap, e.g. 0.5 (0 = unlimited)
	PidsLimit int     // max processes/threads (0 = unlimited); fork-bomb backstop
}

// Instance is a live (or resumable) handle to a running workload.
type Instance struct {
	Ref       string
	State     State
	Addr      string    // host:port the gateway proxies to while running
	StartedAt time.Time // zero when not currently running
}

// Stats is a point-in-time resource snapshot for a running instance. Values are
// human-readable strings straight from the driver (e.g. docker stats) so the UI
// can display them without unit math.
type Stats struct {
	MemUsage string `json:"mem_usage"` // e.g. "76.2MiB / 384MiB"
	MemPerc  string `json:"mem_perc"`  // e.g. "19.8%"
	CPUPerc  string `json:"cpu_perc"`  // e.g. "2.00%"
}

// Runtime is the substrate driver contract. Implementations must be safe for
// concurrent use across refs.
type Runtime interface {
	// Name identifies the driver (e.g. "local", "firecracker") for logging/metrics.
	Name() string

	// Stats returns a live resource snapshot for a running instance.
	Stats(ctx context.Context, ref string) (Stats, error)

	// Logs returns the last `tail` lines of the instance's output.
	Logs(ctx context.Context, ref string, tail int) (string, error)

	// Create provisions a brand-new instance: allocate compute, first boot,
	// initialize the data dir, apply migrations, then leave it Running.
	Create(ctx context.Context, spec Spec) (*Instance, error)

	// Start brings an existing (suspended/stopped) instance back to Running.
	// For LocalDriver this relaunches the process against the same DataDir. For
	// FirecrackerDriver this will restore the per-project memory snapshot, which
	// is where the sub-second wake comes from.
	Start(ctx context.Context, spec Spec) (*Instance, error)

	// Suspend scales an instance to zero: free its memory/compute but keep the
	// data at rest so Start can bring it back. This is the scale-to-zero path.
	Suspend(ctx context.Context, ref string) error

	// Stop halts an instance without the expectation of a fast resume.
	Stop(ctx context.Context, ref string) error

	// Status reports the current lifecycle state of an instance.
	Status(ctx context.Context, ref string) (State, error)
}
