#!/usr/bin/env node
/**
 * End-to-end: an orchd image tarball -> lifo VFS -> browser-metro -> served
 * bundle, with no lifo-specific artifact and no npm install.
 *
 * This is the real rapidnative mobile workspace (expo-router, nativewind,
 * reanimated, gifted-charts), not a toy app.
 */
import { readFileSync } from 'node:fs';
import { performance } from 'node:perf_hooks';
import { Sandbox, importVfsSnapshot } from '/Users/sanketsahu/projects/lifo-sh/lifo/packages/core/dist/index.js';

const TAR = '/Users/sanketsahu/projects/orchd/.localdev/images/rapidnative/v2/base.tar.gz';
const PORT = 8082;
const APP = '/mobile';               // where the orchd tarball's mobile workspace lands
const now = () => performance.now();
const ms = (t) => Math.round(t);
const quiet = () => {};

const bytes = new Uint8Array(readFileSync(TAR));
const t0 = now();
const sb = await Sandbox.create({ persist: false, cwd: '/', env: {} });
const created = now() - t0;

const t1 = now();
await importVfsSnapshot(sb.kernel.vfs, bytes);
const imported = now() - t1;

// The orchd.json that travelled inside the image — what lifo-pkg-orchd reads.
const cfg = JSON.parse(sb.kernel.vfs.readFileString('/orchd.json'));
const wl = cfg.workloads.find((w) => w.name === 'mobile');
console.log(`image: ${(bytes.length / 1024).toFixed(1)} KB | sandbox ${ms(created)}ms | import ${ms(imported)}ms`);
console.log(`orchd.json read from VFS: workload=${wl.name} kind=${wl.kind} dir=${wl.dir}`);
console.log(`  base run    : ${JSON.stringify(wl.run)}`);
console.log(`  lifo profile: ["browser-metro","--port","$PORT"]  (proposed)`);

const t2 = now();
const ac = new AbortController();
sb.shell.execute(`browser-metro ${APP} --port ${PORT}`, { cwd: APP, env: sb.env, onStdout: quiet, onStderr: quiet, signal: ac.signal })
  .catch((e) => console.log('cmd ended:', e.message));

let bound = null;
for (let i = 0; i < 240; i++) {
  if (sb.kernel.portRegistry.has(PORT)) { bound = now() - t2; break; }
  await new Promise((r) => setTimeout(r, 250));
}
console.log(`port ${PORT} bound: ${bound === null ? 'TIMEOUT' : ms(bound) + 'ms'}`);

function vmGet(url) {
  const h = sb.kernel.portRegistry.get(PORT);
  if (!h) return { status: 0, bytes: 0, text: '' };
  const res = { statusCode: 200, headers: {}, body: '' };
  h({ method: 'GET', url, headers: { host: `localhost:${PORT}` }, body: '' }, res);
  return { status: res.statusCode, bytes: Buffer.byteLength(res.body || ''), text: res.body || '' };
}

if (bound !== null) {
  const html = vmGet('/');
  console.log(`GET /            -> ${html.status}, ${html.bytes} bytes, has #root: ${html.text.includes('id="root"')}`);
  const t3 = now();
  let b = vmGet('/index.bundle?platform=web');
  for (let i = 0; i < 240 && !(b.status === 200 && b.bytes > 50000); i++) {
    await new Promise((r) => setTimeout(r, 250));
    b = vmGet('/index.bundle?platform=web');
  }
  console.log(`GET /index.bundle -> ${b.status}, ${(b.bytes / 1024 / 1024).toFixed(2)} MB, ${ms(now() - t3)}ms`);
  console.log(`\nTOTAL image -> serving bundle: ${ms(now() - t0)}ms`);
  console.log(b.status === 200 && b.bytes > 50000 ? '✅ PASS' : '❌ FAIL — bundle not served');
}
ac.abort();
process.exit(0);
