package manager

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/store"
	"github.com/tinbase/tinbase-cloud/orchestrator/internal/template"
)

// imagesDir is where frozen template tarballs live on this instance:
//
//	<DataRoot>/images/<template>/<version>/base.tar.gz
func (m *Manager) imagesDir() string { return filepath.Join(m.cfg.DataRoot, "images") }

func (m *Manager) imagePath(tmpl, version string) string {
	return filepath.Join(m.imagesDir(), tmpl, version, "base.tar.gz")
}

// nextImageVersion returns the next auto-incremented version (v1, v2, …) for a
// template, based on the versions already recorded.
func (m *Manager) nextImageVersion(tmpl string) string {
	max := 0
	for _, im := range m.store.ListImages() {
		if im.Template != tmpl {
			continue
		}
		if n, err := strconv.Atoi(strings.TrimPrefix(im.Version, "v")); err == nil && n > max {
			max = n
		}
	}
	return "v" + strconv.Itoa(max+1)
}

// BuiltImages lists all frozen template images, newest first per template.
func (m *Manager) BuiltImages() []*store.Image { return m.store.ListImages() }

// BuiltImage returns one frozen image by template@version.
func (m *Manager) BuiltImage(tmpl, version string) (*store.Image, error) {
	return m.store.GetImage(tmpl, version)
}

// ImageTarball returns the on-disk path to an image's base tarball, so the API
// can stream it (e.g. to hydrate a client VFS from the frozen base). Errors for
// docker-only / imported images, which have no tarball.
func (m *Manager) ImageTarball(tmpl, version string) (string, error) {
	im, err := m.store.GetImage(tmpl, version)
	if err != nil {
		return "", err
	}
	if im.Tarball == "" {
		return "", fmt.Errorf("image %s is docker-only (no tarball)", store.ImageID(tmpl, version))
	}
	if _, err := os.Stat(im.Tarball); err != nil {
		return "", fmt.Errorf("tarball missing: %w", err)
	}
	return im.Tarball, nil
}

// BuildImage freezes the current state of a template into an immutable, versioned
// image. It always writes a tarball of the template tree (the artifact local /
// process boots restore from); when the `docker` CLI is available it additionally
// builds and tags a container image per node/static workspace (what prod boots).
// The version auto-increments (v1, v2, …). Docker build failures are non-fatal —
// the tarball is the guaranteed artifact — so a box without Docker still produces
// a usable image for local mode.
func (m *Manager) BuildImage(ctx context.Context, tmpl string) (*store.Image, error) {
	// Builds are long (docker builds + warm steps + deps extraction) and their
	// half-finished artifacts poison the image record (observed: a dropped
	// client killed docker cp mid-extract, and every boot of that version paid
	// a multi-minute volume copy). Survive caller cancellation.
	ctx = context.WithoutCancel(ctx)
	lk, _ := m.buildLocks.LoadOrStore(tmpl, &sync.Mutex{})
	lk.(*sync.Mutex).Lock()
	defer lk.(*sync.Mutex).Unlock()
	base := m.templatePath(tmpl)
	if base == "" {
		return nil, fmt.Errorf("template %q is not configured (add it in Settings)", tmpl)
	}
	man, err := template.Load(base)
	if err != nil {
		return nil, fmt.Errorf("template %q: %w", tmpl, err)
	}

	version := m.nextImageVersion(tmpl)
	tarPath := m.imagePath(tmpl, version)
	if err := os.MkdirAll(filepath.Dir(tarPath), 0o755); err != nil {
		return nil, fmt.Errorf("image dir: %w", err)
	}

	// 1) Freeze the tarball (always). This is what local/process boots restore.
	f, err := os.Create(tarPath)
	if err != nil {
		return nil, fmt.Errorf("create tarball: %w", err)
	}
	if err := template.Bundle(base, man.BackupExclude, f); err != nil {
		f.Close()
		os.Remove(tarPath)
		return nil, fmt.Errorf("bundle template: %w", err)
	}
	if err := f.Close(); err != nil {
		return nil, err
	}

	im := &store.Image{
		Template:  tmpl,
		Version:   version,
		Tarball:   tarPath,
		Dockers:   map[string]string{},
		Workloads: imageWorkloads(man),
		CreatedAt: time.Now(),
	}

	// 2) Build a container image per node/static workspace (best-effort). Prod
	// boots these; local ignores them.
	if _, err := exec.LookPath("docker"); err == nil {
		for _, w := range man.Workloads {
			if w.Kind != "node" && w.Kind != "static" {
				continue // tinbase workloads run the platform image, not a per-template build
			}
			tag := fmt.Sprintf("orchd-%s-%s:%s", sanitizeTag(tmpl), sanitizeTag(w.Name), version)
			if err := m.dockerBuildWorkspace(ctx, base, w, tag); err != nil {
				log.Printf("BuildImage %s@%s: workspace %q docker build skipped: %v", tmpl, version, w.Name, err)
				continue
			}
			im.Dockers[w.Name] = tag
			// Firecracker pilot: prepare the microVM base + warm volume for
			// workloads routed to the fc runtime (clean export, one template
			// boot, warm request from the manifest's `warm` path).
			if mx, ok := m.rt.(interface {
				PicksFC(tmpl, workspace string) bool
				PrepareFCImage(ctx context.Context, dockerTag, runCmd, warmPath string) error
			}); ok && mx.PicksFC(tmpl, w.Name) && w.Kind == "node" {
				runCmd := strings.Join(w.Run, " ")
				if len(w.Run) == 3 && w.Run[0] == "sh" {
					runCmd = w.Run[2]
				}
				if err := mx.PrepareFCImage(ctx, tag, runCmd, w.Warm); err != nil {
					log.Printf("BuildImage %s@%s: fc image prep %q skipped: %v", tmpl, version, w.Name, err)
				}
			}
			// Extract the image's installed node_modules once per version. Every
			// workload of this image bind-mounts it read-only — without this,
			// docker's named-volume initialization copies multi-GB of small
			// files per workload on first boot (observed: 10+ minute boots).
			if w.Kind == "node" {
				if err := m.ensureImageDeps(ctx, base, w, tag, tmpl, version); err != nil {
					log.Printf("BuildImage %s@%s: deps extract %q skipped: %v", tmpl, version, w.Name, err)
				}
			}
		}
		// The build cache is inode-dense (each version's node_modules layers
		// are ~80k files) and grew unbounded to 164GB / a quarter of the
		// filesystem's inodes before it starved container creation. Keep
		// enough for warm rebuilds, drop the tail.
		if out, err := exec.CommandContext(ctx, "docker", "builder", "prune", "-f", "--keep-storage", "30GB").CombinedOutput(); err != nil {
			log.Printf("BuildImage %s@%s: builder prune skipped: %v: %s", tmpl, version, err, tailStr(string(out), 120))
		}
	} else {
		log.Printf("BuildImage %s@%s: docker CLI not found, tarball-only image", tmpl, version)
	}

	if err := m.store.PutImage(im); err != nil {
		return nil, err
	}
	m.gcImageVersions(ctx, tmpl, 2)
	m.emit("image.built", "", "", store.ImageID(tmpl, version))
	return im, nil
}

// gcImageVersions removes image versions beyond the newest `keep` that no
// container (running or stopped — suspended workloads must keep waking)
// references: docker tags, the store record, and the versioned data dir.
// Best-effort; a referenced or unparseable version is simply left alone.
func (m *Manager) gcImageVersions(ctx context.Context, tmpl string, keep int) {
	type ver struct {
		n  int
		im *store.Image
	}
	var vers []ver
	for _, im := range m.store.ListImages() {
		if im.Template != tmpl {
			continue
		}
		n, err := strconv.Atoi(strings.TrimPrefix(im.Version, "v"))
		if err != nil {
			continue
		}
		vers = append(vers, ver{n, im})
	}
	sort.Slice(vers, func(i, j int) bool { return vers[i].n > vers[j].n })
	for i, v := range vers {
		if i < keep {
			continue
		}
		referenced := false
		for _, tag := range v.im.Dockers {
			out, err := exec.CommandContext(ctx, "docker", "ps", "-aq", "--filter", "ancestor="+tag).Output()
			if err != nil || len(strings.TrimSpace(string(out))) > 0 {
				referenced = true
				break
			}
		}
		if referenced {
			continue
		}
		for _, tag := range v.im.Dockers {
			_ = exec.CommandContext(ctx, "docker", "rmi", tag).Run()
		}
		if err := m.store.DeleteImage(tmpl, v.im.Version); err != nil {
			log.Printf("image gc %s@%s: store delete: %v", tmpl, v.im.Version, err)
			continue
		}
		_ = os.RemoveAll(filepath.Join(m.imagesDir(), tmpl, v.im.Version))
		log.Printf("image gc: removed %s@%s (unreferenced)", tmpl, v.im.Version)
	}
}

// imageWorkloads freezes a manifest's workload shape into the driver-agnostic
// form stored on an image (so an import needs no template folder).
func imageWorkloads(man *template.Manifest) []store.ImageWorkload {
	out := make([]store.ImageWorkload, 0, len(man.Workloads))
	for _, w := range man.Workloads {
		out = append(out, store.ImageWorkload{
			Name:      w.Name,
			Kind:      w.Kind,
			Workspace: w.Name,
			Image:     w.Image,
			Env:       w.Env,
			Primary:   w.Primary,
			Port:      workspaceImagePort(w),
			Dir:       w.Dir,
		})
	}
	return out
}

// workspaceImagePort is the container port a synthesized workspace image
// listens on (see dockerfileFor): nginx serves static on 80, node runs with
// PORT=8080. tinbase keeps the driver default (54321).
func workspaceImagePort(w template.Workload) int {
	switch w.Kind {
	case "static":
		return 80
	case "node":
		return 8080
	}
	return 0
}

// imagePrimaryName mirrors Manifest.PrimaryName for a frozen image shape:
// the workload marked primary, else (legacy) the first tinbase workload.
func imagePrimaryName(shape []store.ImageWorkload) string {
	for _, w := range shape {
		if w.Primary {
			return w.Name
		}
	}
	for _, w := range shape {
		if w.Kind == "tinbase" {
			return w.Name
		}
	}
	return ""
}

// PushImage re-tags each of an image's local docker tags under the configured
// registry prefix and pushes them, recording the pushed refs. Returns the
// workspace -> registry ref map. Requires a configured registry, the docker CLI,
// and an image that actually built docker tags (not tarball-only).
func (m *Manager) PushImage(ctx context.Context, tmpl, version string) (map[string]string, error) {
	im, err := m.store.GetImage(tmpl, version)
	if err != nil {
		return nil, err
	}
	registry := strings.TrimRight(m.store.GetSettings().Registry, "/")
	if registry == "" {
		return nil, fmt.Errorf("no registry configured (set one in Settings)")
	}
	if len(im.Dockers) == 0 {
		return nil, fmt.Errorf("image %s has no docker images to push (tarball-only)", store.ImageID(tmpl, version))
	}
	if _, err := exec.LookPath("docker"); err != nil {
		return nil, fmt.Errorf("docker CLI not found")
	}
	pushed := map[string]string{}
	for ws, localTag := range im.Dockers {
		ref := fmt.Sprintf("%s/orchd-%s-%s:%s", registry, sanitizeTag(tmpl), sanitizeTag(ws), version)
		if out, err := exec.CommandContext(ctx, "docker", "tag", localTag, ref).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("tag %s: %v: %s", ws, err, tailStr(string(out), 300))
		}
		if out, err := exec.CommandContext(ctx, "docker", "push", ref).CombinedOutput(); err != nil {
			return nil, fmt.Errorf("push %s: %v: %s", ws, err, tailStr(string(out), 300))
		}
		pushed[ws] = ref
	}
	im.Registry = pushed
	if err := m.store.PutImage(im); err != nil {
		return nil, err
	}
	m.emit("image.pushed", "", "", store.ImageID(tmpl, version))
	return pushed, nil
}

// ImportSpec is the self-contained descriptor another instance imports to run a
// frozen image without its tarball or template folder: the registry docker refs
// plus the workload shape.
type ImportSpec struct {
	Template  string                `json:"template"`
	Version   string                `json:"version"`
	Dockers   map[string]string     `json:"dockers"` // workspace -> registry ref (pullable on the target)
	Workloads []store.ImageWorkload `json:"workloads"`
}

// ImageImportSpec produces the ImportSpec for a pushed image — what an operator
// copies to a target ORCHD instance. Requires the image to have been pushed
// (registry refs), since the target must be able to pull it.
func (m *Manager) ImageImportSpec(tmpl, version string) (*ImportSpec, error) {
	im, err := m.store.GetImage(tmpl, version)
	if err != nil {
		return nil, err
	}
	if len(im.Registry) == 0 {
		return nil, fmt.Errorf("image %s has not been pushed to a registry yet", store.ImageID(tmpl, version))
	}
	return &ImportSpec{
		Template:  im.Template,
		Version:   im.Version,
		Dockers:   im.Registry,
		Workloads: im.Workloads,
	}, nil
}

// ImportImage records a frozen image imported from another instance. It is
// docker-only (no tarball): the docker driver pulls the registry refs on boot.
func (m *Manager) ImportImage(spec ImportSpec) (*store.Image, error) {
	if spec.Template == "" || spec.Version == "" {
		return nil, fmt.Errorf("template and version are required")
	}
	if len(spec.Dockers) == 0 {
		return nil, fmt.Errorf("no docker refs to import")
	}
	im := &store.Image{
		Template:  spec.Template,
		Version:   spec.Version,
		Dockers:   spec.Dockers,
		Registry:  spec.Dockers,
		Workloads: spec.Workloads,
		Imported:  true,
		CreatedAt: time.Now(),
	}
	if err := m.store.PutImage(im); err != nil {
		return nil, err
	}
	m.emit("image.imported", "", "", store.ImageID(spec.Template, spec.Version))
	return im, nil
}

// imageDepsDir is where a workspace image's node_modules is extracted for
// shared, read-only mounting into every workload booted from that image:
//
//	<DataRoot>/images/<template>/<version>/deps/<workspace>
func (m *Manager) imageDepsDir(tmpl, version, workspace string) string {
	return filepath.Join(m.imagesDir(), tmpl, version, "deps", workspace)
}

// ensureImageDeps materializes the shared node_modules extraction for one
// workspace of a freshly built image, deduplicated by lockfile content:
// consecutive versions almost always ship identical dependencies, so the
// ~700MB / ~80k-inode tree is extracted once per distinct lockfile into
// <tmpl>/shared-deps/<hash> and each version's deps/<workspace> is a symlink
// to it (overlay lowerdirs and os.Stat both resolve symlinks). No lockfile =
// legacy behavior, a private extraction per version.
func (m *Manager) ensureImageDeps(ctx context.Context, base string, w template.Workload, tag, tmpl, version string) error {
	dest := m.imageDepsDir(tmpl, version, w.Name)
	hash := lockfileHash(base, w.Dir)
	if hash == "" {
		return m.extractImageDeps(ctx, tag, dest)
	}
	shared := filepath.Join(m.imagesDir(), tmpl, "shared-deps", hash+"-"+w.Name)
	if err := m.extractImageDeps(ctx, tag, shared); err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	_ = os.Remove(dest)
	return os.Symlink(shared, dest)
}

// lockfileHash hashes the workspace's dependency manifest (lockfile when
// present, else package.json). Empty when neither exists.
func lockfileHash(base, dir string) string {
	for _, name := range []string{"package-lock.json", "bun.lock", "package.json"} {
		b, err := os.ReadFile(filepath.Join(base, dir, name))
		if err == nil {
			sum := sha256.Sum256(b)
			return hex.EncodeToString(sum[:])[:12]
		}
	}
	return ""
}

// extractImageDeps copies /app/node_modules out of a built workspace image
// into destDir (atomically via a temp dir). The tree is immutable per image
// version, so one extraction serves every workload.
func (m *Manager) extractImageDeps(ctx context.Context, tag, destDir string) error {
	if _, err := os.Stat(destDir); err == nil {
		return nil // already extracted
	}
	tmp := destDir + ".tmp"
	_ = os.RemoveAll(tmp)
	if err := os.MkdirAll(filepath.Dir(destDir), 0o755); err != nil {
		return err
	}
	cid, err := exec.CommandContext(ctx, "docker", "create", tag).Output()
	if err != nil {
		return fmt.Errorf("docker create: %w", err)
	}
	id := strings.TrimSpace(string(cid))
	defer exec.Command("docker", "rm", "-f", id).Run()
	if out, err := exec.CommandContext(ctx, "docker", "cp", id+":/app/node_modules", tmp).CombinedOutput(); err != nil {
		_ = os.RemoveAll(tmp)
		return fmt.Errorf("docker cp: %v: %s", err, tailStr(string(out), 200))
	}
	return os.Rename(tmp, destDir)
}

// dockerBuildWorkspace generates a Dockerfile for one workspace and builds it,
// using the template root as the build context. The generated Dockerfile is kept
// alongside the tarball for transparency.

// sortedKeys returns a map's keys in stable order (nil-safe).
func sortedKeys(m map[string]string) []string {
	keys := make([]string, 0, len(m))
	for k := range m {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func (m *Manager) dockerBuildWorkspace(ctx context.Context, base string, w template.Workload, tag string) error {
	dfContent := dockerfileFor(w, m.cfg.BuildEnv)
	dfName := "Dockerfile.orchd." + sanitizeTag(w.Name)
	dfPath := filepath.Join(base, dfName)
	if err := os.WriteFile(dfPath, []byte(dfContent), 0o644); err != nil {
		return err
	}
	defer os.Remove(dfPath)

	args := []string{"build", "-f", dfPath, "-t", tag}
	// Deterministic order so repeated builds produce identical commands.
	for _, k := range sortedKeys(m.cfg.BuildEnv) {
		args = append(args, "--build-arg", k+"="+m.cfg.BuildEnv[k])
	}
	args = append(args, base)
	cmd := exec.CommandContext(ctx, "docker", args...)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, tailStr(string(out), 400))
	}
	return nil
}

// dockerfileFor generates a container recipe for a node or static workspace from
// its orchd.json entry. The internal port is fixed at 8080; the run command's
// $PORT is expanded by the shell from the PORT env at container start.
//
// buildEnv keys become ARGs declared before the setup/install RUNs, so build
// steps see them in their environment (npm reads npm_config_registry from env)
// while the built image carries none of it — ARG, unlike ENV, does not persist,
// so runtime behaviour stays governed by the workload spec env alone. An empty
// map emits nothing and the Dockerfile is byte-identical to before (layer-cache
// safe).
func dockerfileFor(w template.Workload, buildEnv map[string]string) string {
	var b strings.Builder
	dir := w.Dir
	if dir == "" {
		dir = "."
	}
	switch w.Kind {
	case "static":
		b.WriteString("FROM nginx:alpine\n")
		fmt.Fprintf(&b, "COPY %s/ /usr/share/nginx/html/\n", strings.TrimSuffix(dir, "/"))
		b.WriteString("EXPOSE 80\n")
	default: // node
		// node 22: the earliest LTS with native WebSocket, which supabase-js's
		// realtime client requires at construction — on node 20 a dev server's
		// web SSR dies with "Node.js 20 detected without native WebSocket
		// support" the moment the app builds its client. Every preset image in
		// orchestrator/images is already on 22; this generator was the laggard.
		b.WriteString("FROM node:22-slim\n")
		b.WriteString("WORKDIR /app\n")
		// A stable, writable cache home baked into the image: build steps warm
		// it (transform caches, prebuilt bundles) and runtime containers reuse
		// it via copy-on-write (survives suspend/wake; reset on recreate).
		b.WriteString("ENV HOME=/cache\n")
		b.WriteString("RUN mkdir -p /cache\n")
		for _, k := range sortedKeys(buildEnv) {
			fmt.Fprintf(&b, "ARG %s\n", k)
		}
		// Layer ordering is the size/inode story: everything below COPY is
		// invalidated by ANY template change, so app-independent work stays
		// above it. Setup (toolchains) first; then the manifest lockfile alone
		// so the install layer (typically >1GB / ~80k files) is reused across
		// versions while dependencies are unchanged; the full tree last.
		for _, step := range w.Setup {
			j, _ := json.Marshal(step)
			fmt.Fprintf(&b, "RUN %s\n", j)
		}
		if len(w.Install) > 0 {
			fmt.Fprintf(&b, "COPY %s/package*.json /app/\n", strings.TrimSuffix(dir, "/"))
			fmt.Fprintf(&b, "RUN %s\n", strings.Join(w.Install, " "))
		}
		fmt.Fprintf(&b, "COPY %s/ /app/\n", strings.TrimSuffix(dir, "/"))
		b.WriteString("ENV PORT=8080\n")
		for _, step := range w.Build {
			// Exec-form RUN: argv survives exactly (a step like
			// ["sh","-lc","<script>"] must not be re-parsed by the build shell).
			j, _ := json.Marshal(step)
			fmt.Fprintf(&b, "RUN %s\n", j)
		}
		b.WriteString("EXPOSE 8080\n")
		run := w.Run
		if len(run) == 0 {
			run = []string{"npm", "start"}
		}
		// Exec-form CMD with $PORT pre-substituted: the image port is fixed at
		// 8080, and exec form keeps multi-word argv (e.g. ["sh","-lc",script])
		// intact instead of re-parsing it through a shell.
		sub := make([]string, len(run))
		for i, a := range run {
			sub[i] = strings.ReplaceAll(a, "$PORT", "8080")
		}
		cj, _ := json.Marshal(sub)
		fmt.Fprintf(&b, "CMD %s\n", cj)
	}
	return b.String()
}

// sanitizeTag lowercases and strips characters not allowed in a docker tag.
func sanitizeTag(s string) string {
	s = strings.ToLower(s)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r == '-', r == '_', r == '.':
			b.WriteRune(r)
		default:
			b.WriteRune('-')
		}
	}
	return b.String()
}

func tailStr(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return "…" + s[len(s)-n:]
}

// DeleteImage removes a frozen image's tarball and its store record. Docker image
// tags are left in place (cheap to leave, risky to remove blindly).
func (m *Manager) DeleteImage(tmpl, version string) error {
	im, err := m.store.GetImage(tmpl, version)
	if err != nil {
		return err
	}
	if im.Tarball != "" {
		_ = os.RemoveAll(filepath.Dir(im.Tarball))
	}
	return m.store.DeleteImage(tmpl, version)
}

// imageVersionsSorted returns a template's versions, numeric-descending (newest
// first). Used by callers that want "the latest image for template X".
func (m *Manager) latestImageVersion(tmpl string) (string, bool) {
	var versions []string
	for _, im := range m.store.ListImages() {
		if im.Template == tmpl {
			versions = append(versions, im.Version)
		}
	}
	if len(versions) == 0 {
		return "", false
	}
	sort.Slice(versions, func(i, j int) bool {
		return atoiVer(versions[i]) > atoiVer(versions[j])
	})
	return versions[0], true
}

func atoiVer(v string) int {
	n, _ := strconv.Atoi(strings.TrimPrefix(v, "v"))
	return n
}
