#!/usr/bin/env node
/**
 * bench-snapshots.mjs — measure cold vs warm lifo snapshots for an orchd-style
 * workload, to answer: is the build-time "warm" step worth it?
 *
 * Three artifact variants of the SAME app:
 *   A cold  — source only, no node_modules            (orchd "tarball")
 *   B deps  — node_modules, dev server never ran      (orchd "warm", install only)
 *   C warm  — node_modules + dev server ran once      (orchd "warm", primed)
 *
 * For each: snapshot size, export time, and restore->port-ready time in a fresh
 * sandbox (cold additionally pays npm install on boot).
 */
import http from 'node:http';
import { Sandbox } from '/Users/sanketsahu/projects/lifo-sh/lifo/packages/core/dist/index.js';

const APP = '/home/user/app';
const CORS = 8829;
const DEV_PORT = 5173;
const quiet = () => {};

// CORS proxy the sandbox uses for registry/CDN fetches (same as lifo's benches).
const proxy = http.createServer((q, r) => {
  (async () => {
    const t = new URL(q.url, 'http://x').searchParams.get('url');
    if (!t) { r.statusCode = 400; return r.end('x'); }
    const u = await fetch(t, { headers: { accept: '*/*', 'user-agent': 'lifo' } });
    r.statusCode = u.status;
    r.end(Buffer.from(await u.arrayBuffer()));
  })().catch((e) => { r.statusCode = 502; r.end(e.message); });
});
await new Promise((r) => proxy.listen(CORS, r));
const env = { LIFO_CORS_PROXY: `http://localhost:${CORS}/_cors?url=` };

const files = {
  [`${APP}/package.json`]: JSON.stringify({
    name: 'bench-app', version: '1.0.0', type: 'module',
    scripts: { dev: 'vite --port ' + DEV_PORT, build: 'vite build' },
    dependencies: {
      vite: '^7.3.1', react: '^18.3.1', 'react-dom': '^18.3.1',
      '@vitejs/plugin-react': '^5.0.0', '@supabase/supabase-js': '^2.110.0',
    },
  }, null, 2),
  [`${APP}/index.html`]: `<!doctype html><html><body><div id="root"></div><script type="module" src="/main.jsx"></script></body></html>`,
  [`${APP}/main.jsx`]: `import React from 'react'\nimport { createRoot } from 'react-dom/client'\nimport { createClient } from '@supabase/supabase-js'\ncreateClient('http://localhost:54321','anon')\ncreateRoot(document.getElementById('root')).render(React.createElement('h1', null, 'bench'))\n`,
  [`${APP}/vite.config.js`]: `import react from '@vitejs/plugin-react'\nexport default { plugins: [react()] }\n`,
};

const ms = (t) => Math.round(t);
const mb = (n) => (n / 1024 / 1024).toFixed(2) + ' MB';
const now = () => performance.now();

async function waitPort(sb, port, timeoutMs) {
  const t0 = now();
  while (now() - t0 < timeoutMs) {
    if (sb.kernel.portRegistry.has(port)) return now() - t0;
    await new Promise((r) => setTimeout(r, 100));
  }
  return null;
}

// ---- Build the three source snapshots -------------------------------------
console.log('building source sandbox (npm install)…');
const src = await Sandbox.create({ files, cwd: APP, persist: false, env });

let t = now();
const coldBytes = await src.exportSnapshot({ exclude: ['node_modules', '.git'] });
const coldExport = now() - t;

t = now();
await src.commands.run('npm install', { cwd: APP, timeout: 600000, onStdout: quiet, onStderr: quiet });
const installTime = now() - t;
console.log(`  npm install: ${ms(installTime)} ms`);

t = now();
const depsBytes = await src.exportSnapshot({ exclude: ['.git'] });
const depsExport = now() - t;

// Prime caches: run the dev server once, wait for the port, then stop it.
console.log('priming caches (dev server once)…');
const ac = new AbortController();
src.shell.execute('npm run dev', { cwd: APP, env: src.env, onStdout: quiet, onStderr: quiet, signal: ac.signal })
  .catch(() => {});
const primeReady = await waitPort(src, DEV_PORT, 120000);
console.log(`  dev ready during prime: ${primeReady === null ? 'TIMEOUT' : ms(primeReady) + ' ms'}`);
await new Promise((r) => setTimeout(r, 3000)); // let it finish writing caches
ac.abort();
await new Promise((r) => setTimeout(r, 1000));

t = now();
const warmBytes = await src.exportSnapshot({ exclude: ['.git'] });
const warmExport = now() - t;

// ---- Restore each and measure time to a serving dev server ----------------
async function restoreAndBoot(label, bytes, needsInstall) {
  const sb = await Sandbox.create({ persist: false, cwd: APP, env });
  const t0 = now();
  await sb.importSnapshot(bytes);
  const importTime = now() - t0;

  let install = 0;
  if (needsInstall) {
    const ti = now();
    await sb.commands.run('npm install', { cwd: APP, timeout: 600000, onStdout: quiet, onStderr: quiet });
    install = now() - ti;
  }

  const ac2 = new AbortController();
  const tb = now();
  sb.shell.execute('npm run dev', { cwd: APP, env: sb.env, onStdout: quiet, onStderr: quiet, signal: ac2.signal })
    .catch(() => {});
  const ready = await waitPort(sb, DEV_PORT, 180000);
  const boot = ready === null ? null : now() - tb;
  ac2.abort();
  await sb.destroy?.();

  return { label, size: bytes.length, importTime, install, boot,
           total: importTime + install + (boot ?? NaN) };
}

console.log('\nrestoring…');
const results = [];
results.push(await restoreAndBoot('A cold  (no node_modules)', coldBytes, true));
console.log('  A done');
results.push(await restoreAndBoot('B deps  (node_modules, never run)', depsBytes, false));
console.log('  B done');
results.push(await restoreAndBoot('C warm  (node_modules + primed)', warmBytes, false));
console.log('  C done');

console.log('\n=== snapshot sizes (gzipped tar) ===');
console.log(`A cold : ${mb(coldBytes.length).padStart(9)}   export ${ms(coldExport)} ms`);
console.log(`B deps : ${mb(depsBytes.length).padStart(9)}   export ${ms(depsExport)} ms`);
console.log(`C warm : ${mb(warmBytes.length).padStart(9)}   export ${ms(warmExport)} ms`);

console.log('\n=== restore -> dev server serving ===');
console.log('variant                              import    install       boot      total');
for (const r of results) {
  console.log(
    r.label.padEnd(36) +
    (ms(r.importTime) + 'ms').padStart(8) +
    (r.install ? ms(r.install) + 'ms' : '—').padStart(11) +
    (r.boot === null ? 'TIMEOUT' : ms(r.boot) + 'ms').padStart(11) +
    (Number.isNaN(r.total) ? '—' : ms(r.total) + 'ms').padStart(11)
  );
}

proxy.close();
process.exit(0);
