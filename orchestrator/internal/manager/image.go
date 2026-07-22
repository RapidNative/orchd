package manager

import (
	"context"
	"fmt"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
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

// BuildImage freezes the current state of a template into an immutable, versioned
// image. It always writes a tarball of the template tree (the artifact local /
// process boots restore from); when the `docker` CLI is available it additionally
// builds and tags a container image per node/static workspace (what prod boots).
// The version auto-increments (v1, v2, …). Docker build failures are non-fatal —
// the tarball is the guaranteed artifact — so a box without Docker still produces
// a usable image for local mode.
func (m *Manager) BuildImage(ctx context.Context, tmpl string) (*store.Image, error) {
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
		}
	} else {
		log.Printf("BuildImage %s@%s: docker CLI not found, tarball-only image", tmpl, version)
	}

	if err := m.store.PutImage(im); err != nil {
		return nil, err
	}
	m.emit("image.built", "", "", store.ImageID(tmpl, version))
	return im, nil
}

// dockerBuildWorkspace generates a Dockerfile for one workspace and builds it,
// using the template root as the build context. The generated Dockerfile is kept
// alongside the tarball for transparency.
func (m *Manager) dockerBuildWorkspace(ctx context.Context, base string, w template.Workload, tag string) error {
	dfContent := dockerfileFor(w)
	dfName := "Dockerfile.orchd." + sanitizeTag(w.Name)
	dfPath := filepath.Join(base, dfName)
	if err := os.WriteFile(dfPath, []byte(dfContent), 0o644); err != nil {
		return err
	}
	defer os.Remove(dfPath)

	cmd := exec.CommandContext(ctx, "docker", "build", "-f", dfPath, "-t", tag, base)
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("%v: %s", err, tailStr(string(out), 400))
	}
	return nil
}

// dockerfileFor generates a container recipe for a node or static workspace from
// its orchd.json entry. The internal port is fixed at 8080; the run command's
// $PORT is expanded by the shell from the PORT env at container start.
func dockerfileFor(w template.Workload) string {
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
		b.WriteString("FROM node:20-slim\n")
		b.WriteString("WORKDIR /app\n")
		fmt.Fprintf(&b, "COPY %s/ /app/\n", strings.TrimSuffix(dir, "/"))
		if len(w.Install) > 0 {
			fmt.Fprintf(&b, "RUN %s\n", strings.Join(w.Install, " "))
		}
		b.WriteString("ENV PORT=8080\n")
		b.WriteString("EXPOSE 8080\n")
		run := w.Run
		if len(run) == 0 {
			run = []string{"npm", "start"}
		}
		fmt.Fprintf(&b, "CMD [\"sh\",\"-c\",%q]\n", strings.Join(run, " "))
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
