// Package adopter holds what a product built on ORCHD supplies about itself: the
// workload presets it offers, how its app workloads are labelled, how the local
// process driver runs its images, the readiness probe its dev servers want, and
// a few environment conventions. The engine has no built-in product; without an
// adopter file it runs tinbase databases and generic app workloads only.
//
// The file is JSON, named by ORCHD_ADOPTER_CONFIG. An example for one adopter
// lives in deploy/adopters/.
package adopter

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// DefaultAppWorkloadType labels app (non-database) workloads when the adopter
// doesn't name its own.
const DefaultAppWorkloadType = "app"

// Config is an adopter's settings. The zero value is the neutral engine.
type Config struct {
	// AppWorkloadType is the type recorded on new app workloads (template and
	// preset workloads that aren't databases). Clients that read a workload's
	// type see this value. Empty means DefaultAppWorkloadType.
	AppWorkloadType string `json:"appWorkloadType,omitempty"`

	// Presets are named workload shapes the API accepts as `preset` (the
	// built-in "tinbase" preset always exists). A preset's Type defaults to
	// AppWorkloadType.
	Presets map[string]Preset `json:"presets,omitempty"`

	// ProcessRecipes tell the local (process) driver how to run an image
	// without Docker, keyed by image ref (e.g. "acme-web:dev").
	ProcessRecipes map[string]Recipe `json:"processRecipes,omitempty"`

	// ReadyProbeHeaders are sent with every readiness probe (GET /) while a
	// workload boots. Some dev servers answer a plain GET with an expensive
	// render; a header can ask for something cheap instead.
	ReadyProbeHeaders map[string]string `json:"readyProbeHeaders,omitempty"`

	// ServicesKeyEnv names the project env variable that carries a project's
	// key for the platform's shared services (the SMTP relay and SMS gateway
	// tinbase workloads are pointed at). Required when either is configured.
	ServicesKeyEnv string `json:"servicesKeyEnv,omitempty"`

	// EnvMirrors copy a variable to a second name after a workload's env is
	// resolved, when the second name isn't set already (source -> target).
	EnvMirrors map[string]string `json:"envMirrors,omitempty"`

	// SiteURLWorkload names the workload (by workspace or name) that serves a
	// project's app to its users. Its public URL becomes the database's site
	// URL, so auth links that can't be honoured land in the app rather than on
	// the database's own origin. Empty leaves the site URL alone.
	SiteURLWorkload string `json:"siteUrlWorkload,omitempty"`

	// ExternalProjectIDSuffixes are stripped from a project name before it is
	// read as the adopter's own project id (a UUID) for usage reports.
	ExternalProjectIDSuffixes []string `json:"externalProjectIdSuffixes,omitempty"`
}

// Preset is a named workload shape.
type Preset struct {
	Type  string `json:"type,omitempty"`
	Name  string `json:"name,omitempty"`
	Image string `json:"image,omitempty"`
	Port  int    `json:"port,omitempty"`
}

// Recipe runs an image as a plain OS process for the local driver: starter
// files written on first boot (existing files are kept), a one-time install,
// and a run command. "${PORT}" in Run and Env is replaced by the assigned port.
type Recipe struct {
	Files   map[string]string `json:"files,omitempty"`
	Install []string          `json:"install,omitempty"`
	Run     []string          `json:"run"`
	Env     []string          `json:"env,omitempty"`
}

// AppType is the label for new app workloads.
func (c Config) AppType() string {
	if c.AppWorkloadType != "" {
		return c.AppWorkloadType
	}
	return DefaultAppWorkloadType
}

// Load reads an adopter file. An empty path is the neutral engine.
func Load(path string) (Config, error) {
	if path == "" {
		return Config{}, nil
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return Config{}, fmt.Errorf("adopter config: %w", err)
	}
	var c Config
	dec := json.NewDecoder(strings.NewReader(string(b)))
	dec.DisallowUnknownFields() // a misspelt key must not silently drop a setting
	if err := dec.Decode(&c); err != nil {
		return Config{}, fmt.Errorf("adopter config %s: %w", path, err)
	}
	if err := c.Validate(); err != nil {
		return Config{}, fmt.Errorf("adopter config %s: %w", path, err)
	}
	return c, nil
}

// Validate checks an adopter config for mistakes that would boot and then fail.
func (c Config) Validate() error {
	if c.AppWorkloadType == "tinbase-project" {
		return fmt.Errorf("appWorkloadType cannot be the database type %q", c.AppWorkloadType)
	}
	for name, p := range c.Presets {
		if name == "" || name == "tinbase" {
			return fmt.Errorf("preset name %q is reserved", name)
		}
		if p.Port < 0 || p.Port > 65535 {
			return fmt.Errorf("preset %q: port %d out of range", name, p.Port)
		}
	}
	for image, r := range c.ProcessRecipes {
		if len(r.Run) == 0 {
			return fmt.Errorf("process recipe %q: run is empty", image)
		}
	}
	for src, dst := range c.EnvMirrors {
		if src == "" || dst == "" || src == dst {
			return fmt.Errorf("envMirrors: %q -> %q is not a valid mirror", src, dst)
		}
	}
	return nil
}

// MirrorSources returns the mirror sources in a stable order.
func (c Config) MirrorSources() []string {
	out := make([]string, 0, len(c.EnvMirrors))
	for k := range c.EnvMirrors {
		out = append(out, k)
	}
	sort.Strings(out)
	return out
}
