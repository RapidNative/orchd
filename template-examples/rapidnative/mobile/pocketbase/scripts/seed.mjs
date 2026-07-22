#!/usr/bin/env node
/**
 * PocketBase Seed Runner
 * Imports seed data from pocketbase/seeds/index.mjs and creates records via REST API.
 * To add a new seed, create a file in seeds/ and register it in seeds/index.mjs.
 *
 * Authenticates as superuser using POCKETBASE_SERVICE_ROLE_KEY from .env.
 *
 * Usage:
 *   node pocketbase/scripts/seed.mjs
 */

import seeds from "../seeds/index.mjs";
import { resolveEnv } from "./env.mjs";

const { base: BASE, authHeaders } = await resolveEnv();

// ── REST helper ──────────────────────────────────────────────────────────

async function createRecord(collection, body) {
  const res = await fetch(`${BASE}/api/collections/${collection}/records`, {
    method: "POST",
    headers: { "Content-Type": "application/json", ...authHeaders },
    body: JSON.stringify(body),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(`Failed to create ${collection} record: ${JSON.stringify(data)}`);
  return data;
}

// ── Main ─────────────────────────────────────────────────────────────────

async function main() {
  console.log(`Using PocketBase URL: ${BASE}\n`);
  console.log(`Found ${seeds.length} seed files.\n`);

  const ctx = {};

  for (const seed of seeds) {
    const name = seed.collection;
    console.log(`Seeding ${name}...`);

    const rows = seed.records(ctx);
    const created = [];
    for (const row of rows) {
      created.push(await createRecord(name, row));
    }

    ctx[name] = created;
    console.log(`  ✓ ${created.length} ${name}`);
  }

  console.log("\n✓ All seed data created!");
}

main().catch((err) => {
  console.error("\n✗ Error:", err.message);
  process.exit(1);
});
