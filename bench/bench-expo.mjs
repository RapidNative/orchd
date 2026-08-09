#!/usr/bin/env node
/**
 * bench-expo.mjs — the Metro case. Same three artifact variants as the Vite
 * bench, but for an Expo app served by browser-metro (the `profiles.lifo` run
 * command in the orchd.json design).
 *
 * Key question: does browser-metro need node_modules at all (it fetches npm
 * packages pre-bundled from a hosted server), and does a primed bundle cache
 * survive a snapshot?
 */
import { performance } from 'node:perf_hooks';
import { Sandbox } from '/Users/sanketsahu/projects/lifo-sh/lifo/packages/core/dist/index.js';

const APP = '/home/user/my-app';
const PORT = 8082;
const quiet = () => {};
const now = () => performance.now();
const ms = (t) => Math.round(t);
const mb = (n) => (n / 1024 / 1024).toFixed(2) + ' MB';

const files = {
  [`${APP}/package.json`]: JSON.stringify({
    name: 'demo', main: 'index.tsx',
    dependencies: { expo: '~54.0.0', react: '19.1.0', 'react-dom': '19.1.0', 'react-native': '0.81.5', 'react-native-web': '~0.21.0' },
  }),
  [`${APP}/index.tsx`]: `import { registerRootComponent } from 'expo';\nimport App from './App';\nregisterRootComponent(App);\n`,
  [`${APP}/App.tsx`]: `import { Text, View } from 'react-native';\nexport default function App(){ return (<View><Text>Hello from browser-metro in Lifo</Text></View>); }\n`,
};

async function waitPort(sb, port, timeoutMs) {
  const t0 = now();
  while (now() - t0 < timeoutMs) {
    if (sb.kernel.portRegistry.has(port)) return now() - t0;
    await new Promise((r) => setTimeout(r, 100));
  }
  return null;
}

// A bound port isn't enough for Metro — the bundle must actually build.
async function waitBundle(sb, port, timeoutMs) {
  const t0 = now();
  while (now() - t0 < timeoutMs) {
    const h = sb.kernel.portRegistry.get(port);
    if (h) {
      const res = { statusCode: 200, headers: {}, body: '' };
      try {
        h({ method: 'GET', url: '/index.bundle?platform=web', headers: { host: `localhost:${port}` }, body: '' }, res);
        const len = Buffer.byteLength(res.body || '');
        if (res.statusCode === 200 && len > 50000) return { t: now() - t0, bytes: len };
      } catch { /* keep polling */ }
    }
    await new Promise((r) => setTimeout(r, 250));
  }
  return null;
}

console.log('building source sandbox…');
const src = await Sandbox.create({ files, cwd: APP, persist: false, env: {} });

let t = now();
const coldBytes = await src.exportSnapshot({ exclude: ['node_modules', '.git'] });
const coldExport = now() - t;
console.log(`  cold snapshot: ${mb(coldBytes.length)} (${ms(coldExport)} ms)`);

console.log('npm install (for the deps/warm variants)…');
t = now();
let installOk = true;
try {
  await src.commands.run('npm install', { cwd: APP, timeout: 900000, onStdout: quiet, onStderr: quiet });
} catch (e) { installOk = false; console.log('  install failed:', e.message); }
const installTime = now() - t;
console.log(`  npm install: ${ms(installTime)} ms ok=${installOk}`);

t = now();
const depsBytes = await src.exportSnapshot({ exclude: ['.git'] });
const depsExport = now() - t;
console.log(`  deps snapshot: ${mb(depsBytes.length)} (${ms(depsExport)} ms)`);

console.log('priming (browser-metro once)…');
const ac = new AbortController();
src.shell.execute(`browser-metro ${APP} --port ${PORT}`, { cwd: APP, env: src.env, onStdout: quiet, onStderr: quiet, signal: ac.signal }).catch(() => {});
const primed = await waitBundle(src, PORT, 300000);
console.log(`  primed bundle: ${primed ? ms(primed.t) + ' ms, ' + mb(primed.bytes) : 'TIMEOUT'}`);
await new Promise((r) => setTimeout(r, 3000));
ac.abort();
await new Promise((r) => setTimeout(r, 1000));

t = now();
const warmBytes = await src.exportSnapshot({ exclude: ['.git'] });
const warmExport = now() - t;
console.log(`  warm snapshot: ${mb(warmBytes.length)} (${ms(warmExport)} ms)`);

async function restoreAndBoot(label, bytes, needsInstall) {
  const sb = await Sandbox.create({ persist: false, cwd: APP, env: {} });
  const t0 = now();
  await sb.importSnapshot(bytes);
  const importTime = now() - t0;

  let install = 0;
  if (needsInstall) {
    const ti = now();
    try { await sb.commands.run('npm install', { cwd: APP, timeout: 900000, onStdout: quiet, onStderr: quiet }); }
    catch { /* record what we get */ }
    install = now() - ti;
  }

  const ac2 = new AbortController();
  const tb = now();
  sb.shell.execute(`browser-metro ${APP} --port ${PORT}`, { cwd: APP, env: sb.env, onStdout: quiet, onStderr: quiet, signal: ac2.signal }).catch(() => {});
  const bound = await waitPort(sb, PORT, 180000);
  const bundle = await waitBundle(sb, PORT, 300000);
  ac2.abort();
  await sb.destroy?.();
  return { label, size: bytes.length, importTime, install, bound, bundle };
}

console.log('\nrestoring…');
const results = [];
results.push(await restoreAndBoot('A cold  (no node_modules)', coldBytes, false));
console.log('  A done');
results.push(await restoreAndBoot('B deps  (node_modules, never run)', depsBytes, false));
console.log('  B done');
results.push(await restoreAndBoot('C warm  (node_modules + primed)', warmBytes, false));
console.log('  C done');

console.log('\n=== EXPO / browser-metro ===');
console.log('sizes:');
console.log(`  A cold : ${mb(coldBytes.length).padStart(9)}`);
console.log(`  B deps : ${mb(depsBytes.length).padStart(9)}`);
console.log(`  C warm : ${mb(warmBytes.length).padStart(9)}`);
console.log('\nrestore -> bundle served:');
console.log('variant                                import   port-bound   bundle-ready   bundle-size');
for (const r of results) {
  console.log(
    r.label.padEnd(36) +
    (ms(r.importTime) + 'ms').padStart(9) +
    (r.bound === null ? 'TIMEOUT' : ms(r.bound) + 'ms').padStart(13) +
    (r.bundle === null ? 'TIMEOUT' : ms(r.bundle.t) + 'ms').padStart(15) +
    (r.bundle === null ? '—' : mb(r.bundle.bytes)).padStart(14)
  );
}
process.exit(0);
