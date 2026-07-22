package runtime

import (
	"os"
	"path/filepath"
	"strconv"
)

// This file makes the process (local) driver able to run the RapidNative dev
// apps — not just tinbase — as ordinary OS processes, with no Docker. Each
// workload kind has a "recipe": starter files scaffolded into the workload's
// data dir on first boot, a one-time install step, and a run command that binds
// the workload's assigned port. Local dev thus runs the real dev servers
// (Vite/Hono/Expo) directly; prod still uses the container images.

// localKind classifies a workload for the process driver. "" = no local recipe
// (the driver refuses it and points at the docker driver).
func localKind(spec Spec) string {
	if spec.Type == WorkloadTinbaseProject {
		return "tinbase"
	}
	switch spec.Image {
	case "rn-vite:dev":
		return "vite"
	case "rn-api:dev":
		return "api"
	case "rn-expo:dev":
		return "expo"
	}
	return ""
}

// appRecipe describes how to run a non-tinbase workload locally.
type appRecipe struct {
	files   map[string]string // scaffolded on first boot (relative path -> contents)
	install []string          // one-time setup (e.g. npm install), run in the dir
	run     func(port int) (argv []string, env []string)
}

// recipeFor returns the recipe for an app kind (vite/api/expo). tinbase is
// handled directly by the driver, not here.
func recipeFor(kind string) (appRecipe, bool) {
	switch kind {
	case "vite":
		return appRecipe{
			files: map[string]string{
				"package.json": `{
  "name": "rn-web",
  "private": true,
  "type": "module",
  "scripts": { "dev": "vite" },
  "devDependencies": { "vite": "^5.4.0" }
}
`,
				"index.html": `<!doctype html>
<html>
  <head><meta charset="utf-8" /><title>RapidNative web</title></head>
  <body>
    <h1>RapidNative web (Vite) — local</h1>
    <p>Edit <code>index.html</code> / <code>main.js</code> and HMR reloads.</p>
    <script type="module" src="/main.js"></script>
  </body>
</html>
`,
				"main.js": `console.log('rn-web dev server up')
document.body.insertAdjacentHTML('beforeend', '<p>running · ' + new Date().toISOString() + '</p>')
`,
			},
			install: []string{"npm", "install", "--no-audit", "--no-fund"},
			run: func(port int) ([]string, []string) {
				return []string{
					"npm", "run", "dev", "--",
					"--host", "127.0.0.1", "--port", strconv.Itoa(port), "--strictPort",
				}, nil
			},
		}, true

	case "api":
		return appRecipe{
			files: map[string]string{
				"package.json": `{
  "name": "rn-api",
  "private": true,
  "type": "module",
  "scripts": { "start": "node server.mjs" },
  "dependencies": { "hono": "^4.6.0", "@hono/node-server": "^1.13.0" }
}
`,
				"server.mjs": `import { serve } from '@hono/node-server'
import { Hono } from 'hono'

const app = new Hono()
app.get('/', (c) => c.json({ service: 'rn-api', ok: true, ts: Date.now() }))
app.get('/health', (c) => c.text('ok'))

const port = Number(process.env.PORT) || 3000
serve({ fetch: app.fetch, hostname: '127.0.0.1', port })
console.log('rn-api (Hono) listening on ' + port)
`,
			},
			install: []string{"npm", "install", "--no-audit", "--no-fund"},
			run: func(port int) ([]string, []string) {
				return []string{"node", "server.mjs"}, []string{"PORT=" + strconv.Itoa(port)}
			},
		}, true

	case "expo":
		return appRecipe{
			files: map[string]string{
				"package.json": `{
  "name": "rn-app",
  "private": true,
  "main": "node_modules/expo/AppEntry.js",
  "scripts": { "start": "expo start" },
  "dependencies": {
    "expo": "~51.0.0",
    "expo-status-bar": "~1.12.1",
    "react": "18.2.0",
    "react-native": "0.74.5",
    "react-native-web": "~0.19.10",
    "react-dom": "18.2.0"
  }
}
`,
				"app.json": `{ "expo": { "name": "rn-app", "slug": "rn-app", "web": { "bundler": "metro" } } }
`,
				"App.js": `import { Text, View } from 'react-native'
export default function App() {
  return (
    <View style={{ flex: 1, alignItems: 'center', justifyContent: 'center' }}>
      <Text>RapidNative app (Expo) — local</Text>
    </View>
  )
}
`,
			},
			install: []string{"npm", "install", "--no-audit", "--no-fund"},
			// Expo web dev server on the assigned port. First install is large.
			run: func(port int) ([]string, []string) {
				return []string{"npx", "expo", "start", "--web", "--port", strconv.Itoa(port)},
					[]string{"CI=1", "BROWSER=none"}
			},
		}, true
	}
	return appRecipe{}, false
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
