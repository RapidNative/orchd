package runtime

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/adopter"
)

// This file lets the process (local) driver run app workloads, not just
// tinbase, as ordinary OS processes with no Docker. The engine ships no
// recipes: the adopter supplies them (adopter.Config.ProcessRecipes), keyed by
// the image a workload would run in production. A recipe has starter files
// scaffolded into the workload's data dir on first boot, a one-time install
// step, and a run command that binds the workload's assigned port.

var (
	recipesMu sync.RWMutex
	recipes   map[string]adopter.Recipe
)

// SetProcessRecipes installs the adopter's local recipes (image ref -> recipe).
func SetProcessRecipes(r map[string]adopter.Recipe) {
	recipesMu.Lock()
	defer recipesMu.Unlock()
	recipes = r
}

// localKind classifies a workload for the process driver: "tinbase", the image
// ref of an app workload that has a recipe, or "" (no local recipe: the driver
// refuses it and points at the docker driver).
func localKind(spec Spec) string {
	if spec.Type == WorkloadTinbaseProject {
		return "tinbase"
	}
	recipesMu.RLock()
	defer recipesMu.RUnlock()
	if _, ok := recipes[spec.Image]; ok {
		return spec.Image
	}
	return ""
}

// appRecipe describes how to run an app workload locally.
type appRecipe struct {
	files   map[string]string // scaffolded on first boot (relative path -> contents)
	install []string          // one-time setup (e.g. npm install), run in the dir
	run     func(port int) (argv []string, env []string)
}

// recipeFor returns the recipe for an app kind (an image ref from localKind).
// tinbase is handled directly by the driver, not here.
func recipeFor(kind string) (appRecipe, bool) {
	recipesMu.RLock()
	r, ok := recipes[kind]
	recipesMu.RUnlock()
	if !ok || len(r.Run) == 0 {
		return appRecipe{}, false
	}
	return appRecipe{
		files:   r.Files,
		install: r.Install,
		run: func(port int) ([]string, []string) {
			p := strconv.Itoa(port)
			return withPort(r.Run, p), withPort(r.Env, p)
		},
	}, true
}

func withPort(in []string, port string) []string {
	if in == nil {
		return nil
	}
	out := make([]string, len(in))
	for i, s := range in {
		out[i] = strings.ReplaceAll(s, "${PORT}", port)
	}
	return out
}

// scaffold writes the recipe's starter files into dir for any that don't exist
// yet, so re-boots keep user edits.
func (r appRecipe) scaffold(dir string) error {
	for rel, content := range r.files {
		p := filepath.Join(dir, rel)
		if _, err := os.Stat(p); err == nil {
			continue // keep existing (edited) files
		}
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			return err
		}
	}
	return nil
}
