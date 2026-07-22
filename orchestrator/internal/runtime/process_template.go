package runtime

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/tinbase/tinbase-cloud/orchestrator/internal/template"
)

// bootTemplateCmd materializes a template-based workload: on first boot it copies
// the template folder into DataDir (minus derived dirs), then builds the run
// command for the workload's orchd.json workspace, bound to the assigned port.
// The copy is the per-workload working tree — the user's editable files and the
// backup delta.
func (d *LocalDriver) bootTemplateCmd(spec Spec, port int, out io.Writer) (*exec.Cmd, error) {
	m, err := template.Load(spec.TemplateSrc)
	if err != nil {
		return nil, fmt.Errorf("template manifest: %w", err)
	}
	ws, ok := m.Find(spec.Workspace)
	if !ok {
		return nil, fmt.Errorf("workspace %q not in template %q", spec.Workspace, spec.TemplateSrc)
	}

	// Seed the working copy once. node_modules/.git are always skipped; the rest
	// of backup_exclude keeps the copy lean.
	marker := filepath.Join(spec.DataDir, ".orchd-seeded")
	if _, err := os.Stat(marker); err != nil {
		fmt.Fprintf(out, "\n[orchd] seeding %q from template %s…\n", spec.Workspace, spec.TemplateSrc)
		excl := map[string]bool{"node_modules": true, ".git": true}
		for _, e := range m.BackupExclude {
			excl[e] = true
		}
		if err := copyTree(spec.TemplateSrc, spec.DataDir, excl); err != nil {
			return nil, fmt.Errorf("seed template: %w", err)
		}
		_ = os.WriteFile(marker, []byte("ok\n"), 0o644)
	}

	runDir := spec.DataDir
	if ws.Dir != "" {
		runDir = filepath.Join(spec.DataDir, ws.Dir)
	}

	var cmd *exec.Cmd
	switch ws.Kind {
	case "tinbase":
		args := []string{
			"start", "--port", fmt.Sprint(port),
			"--dir", runDir, "--data-dir", filepath.Join(spec.DataDir, "db"),
		}
		if d.Engine != "" {
			args = append(args, "--engine", d.Engine)
		}
		cmd = d.command(args)
	case "static":
		script := filepath.Join(spec.DataDir, ".orchd-static.mjs")
		if err := os.WriteFile(script, []byte(staticServerJS), 0o644); err != nil {
			return nil, err
		}
		cmd = exec.Command("node", script)
		cmd.Env = append(os.Environ(),
			"PORT="+fmt.Sprint(port), "ORCHD_STATIC_DIR="+runDir)
	case "node":
		if err := ensureInstalled(runDir, ws.Install, out); err != nil {
			return nil, fmt.Errorf("install %q: %w", spec.Workspace, err)
		}
		argv := ws.RunArgv(port)
		cmd = exec.Command(argv[0], argv[1:]...)
		if ws.PortEnv != "" {
			cmd.Env = append(os.Environ(), ws.PortEnv+"="+fmt.Sprint(port))
		}
	default:
		return nil, fmt.Errorf("workspace %q has unsupported kind %q", spec.Workspace, ws.Kind)
	}
	cmd.Dir = runDir
	return cmd, nil
}

// copyTree recursively copies src to dst, skipping any directory whose base name
// is in skip. Files keep their mode.
func copyTree(src, dst string, skip map[string]bool) error {
	return filepath.WalkDir(src, func(p string, de os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(src, p)
		if de.IsDir() {
			if rel != "." && skip[de.Name()] {
				return filepath.SkipDir
			}
			return os.MkdirAll(filepath.Join(dst, rel), 0o755)
		}
		if skip[de.Name()] {
			return nil
		}
		return copyFile(p, filepath.Join(dst, rel))
	})
}

func copyFile(src, dst string) error {
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()
	info, err := in.Stat()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return err
	}
	out, err := os.OpenFile(dst, os.O_CREATE|os.O_TRUNC|os.O_WRONLY, info.Mode())
	if err != nil {
		return err
	}
	defer out.Close()
	_, err = io.Copy(out, in)
	return err
}

// staticServerJS is a zero-dependency Node static file server (SPA-friendly:
// unknown paths fall back to index.html). Serves ORCHD_STATIC_DIR on PORT.
const staticServerJS = `import http from 'node:http'
import { readFile, stat } from 'node:fs/promises'
import { join, extname, normalize, resolve } from 'node:path'

const dir = resolve(process.env.ORCHD_STATIC_DIR || '.')
const port = Number(process.env.PORT) || 8080
const types = { '.html':'text/html','.js':'text/javascript','.mjs':'text/javascript','.css':'text/css','.json':'application/json','.svg':'image/svg+xml','.png':'image/png','.jpg':'image/jpeg','.jpeg':'image/jpeg','.ico':'image/x-icon','.woff2':'font/woff2' }

http.createServer(async (req, res) => {
  try {
    let p = normalize(decodeURIComponent((req.url || '/').split('?')[0]))
    if (p.endsWith('/')) p += 'index.html'
    let f = resolve(join(dir, p))
    if (!f.startsWith(dir)) { res.writeHead(403); return res.end('forbidden') }
    const s = await stat(f).catch(() => null)
    if (!s || !s.isFile()) f = join(dir, 'index.html')
    const body = await readFile(f)
    res.writeHead(200, { 'content-type': types[extname(f)] || 'application/octet-stream' })
    res.end(body)
  } catch { res.writeHead(404); res.end('not found') }
}).listen(port, '127.0.0.1', () => console.log('orchd-static serving ' + dir + ' on ' + port))
`
