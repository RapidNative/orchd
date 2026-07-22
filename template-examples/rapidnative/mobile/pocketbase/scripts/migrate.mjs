#!/usr/bin/env node
/**
 * PocketBase Migration Runner
 * Reads migration files from migrations/ and creates collections via REST API.
 * No collection definitions are duplicated — migrations is the single source of truth.
 *
 * Authenticates as superuser using POCKETBASE_SERVICE_ROLE_KEY from .env.
 *
 * Usage:
 *   node pocketbase/scripts/migrate.mjs
 */

import { readFileSync, readdirSync } from 'fs';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';
import { resolveEnv } from './env.mjs';

const __dirname = dirname(fileURLToPath(import.meta.url));
const MIGRATIONS_DIR = resolve(__dirname, '../migrations');

const { base: BASE, authHeaders } = await resolveEnv();

// ── Parse migration files ────────────────────────────────────────────────

function parseMigrations() {
  const creates = [];
  const alters = [];

  const files = readdirSync(MIGRATIONS_DIR)
    .filter((f) => f.endsWith('.js'))
    .sort();

  for (const file of files) {
    const content = readFileSync(resolve(MIGRATIONS_DIR, file), 'utf-8');
    const isAlter = file.includes('_alter_');

    // Mock PocketBase globals to capture collection configs
    let captured = null;

    function Collection(config) {
      Object.assign(this, config);
    }

    function Field(config) {
      Object.assign(this, config);
    }

    const migrate = (up) => {
      if (isAlter) {
        // For alter migrations, mock findCollectionByNameOrId to return a mutable collection
        const mockCollection = {
          name: null,
          fields: {
            _items: [],
            get length() { return this._items.length; },
            addAt(_pos, field) { this._items.push(field); },
            removeByName(name) { this._items = this._items.filter(f => f.name !== name); },
          },
        };
        const mockApp = {
          findCollectionByNameOrId(nameOrId) {
            mockCollection.name = nameOrId;
            return mockCollection;
          },
          save(collection) {
            captured = {
              name: collection.name,
              _alter: true,
              fields: collection.fields._items,
              listRule: collection.listRule,
              viewRule: collection.viewRule,
              createRule: collection.createRule,
              updateRule: collection.updateRule,
              deleteRule: collection.deleteRule,
            };
          },
        };
        up(mockApp);
      } else {
        const mockApp = {
          save(collection) { captured = collection; },
        };
        up(mockApp);
      }
    };

    // Evaluate migration in a function scope with mocked globals
    const fn = new Function('migrate', 'Collection', 'Field', content);
    fn(migrate, Collection, Field);

    if (captured) {
      if (captured._alter) {
        alters.push(captured);
        console.log(`  Parsed: ${file} -> alter ${captured.name}`);
      } else {
        creates.push(captured);
        console.log(`  Parsed: ${file} -> create ${captured.name}`);
      }
    } else {
      console.log(`  Skipped: ${file} (no collection found)`);
    }
  }

  return { creates, alters };
}

// ── REST helpers ─────────────────────────────────────────────────────────

async function getCollectionByName(name) {
  const res = await fetch(`${BASE}/api/collections/${name}`, {
    headers: authHeaders,
  });
  return res.ok ? res.json() : null;
}

async function createCollection(body) {
  const existing = await getCollectionByName(body.name);
  if (existing) {
    console.log(`  -> ${body.name} already exists (${existing.id}), skipping`);
    return existing;
  }
  const res = await fetch(`${BASE}/api/collections`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json', ...authHeaders },
    body: JSON.stringify(body),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(`Failed to create "${body.name}": ${JSON.stringify(data)}`);
  console.log(`  ✓ ${body.name} created (${data.id})`);
  return data;
}

async function alterCollection(alter) {
  const existing = await getCollectionByName(alter.name);
  if (!existing) throw new Error(`Cannot alter "${alter.name}" — collection not found`);

  // Merge new fields with existing, skip duplicates
  const existingFieldNames = new Set(existing.fields?.map((f) => f.name) || []);
  const newFields = (alter.fields || []).filter((f) => !existingFieldNames.has(f.name));

  if (newFields.length === 0) {
    console.log(`  -> ${alter.name} already has all fields, skipping`);
    return existing;
  }

  const mergedFields = [...(existing.fields || []), ...newFields];
  const patch = { fields: mergedFields };
  if (alter.listRule !== undefined) patch.listRule = alter.listRule;
  if (alter.viewRule !== undefined) patch.viewRule = alter.viewRule;
  if (alter.createRule !== undefined) patch.createRule = alter.createRule;
  if (alter.updateRule !== undefined) patch.updateRule = alter.updateRule;
  if (alter.deleteRule !== undefined) patch.deleteRule = alter.deleteRule;

  const res = await fetch(`${BASE}/api/collections/${existing.id}`, {
    method: 'PATCH',
    headers: { 'Content-Type': 'application/json', ...authHeaders },
    body: JSON.stringify(patch),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(`Failed to alter "${alter.name}": ${JSON.stringify(data)}`);
  console.log(`  ✓ ${alter.name} altered — added fields: ${newFields.map((f) => f.name).join(', ')}`);
  return data;
}

// ── Resolve relation collectionIds ───────────────────────────────────────

async function resolveCollectionIds() {
  const idMap = {};

  const usersCol = await getCollectionByName('users');
  if (!usersCol) throw new Error("Built-in 'users' collection not found");
  idMap['_pb_users_auth_'] = usersCol.id;
  idMap['users'] = usersCol.id;
  console.log(`  Users collection ID: ${usersCol.id}\n`);

  return idMap;
}

function replaceCollectionIds(collectionConfig, idMap) {
  if (!collectionConfig.fields) return collectionConfig;

  const resolved = { ...collectionConfig };
  resolved.fields = collectionConfig.fields.map((field) => {
    if (field.type === 'relation' && field.collectionId) {
      const realId = idMap[field.collectionId];
      if (realId) {
        return { ...field, collectionId: realId };
      }
    }
    return field;
  });
  return resolved;
}

// ── Main ─────────────────────────────────────────────────────────────────

async function main() {
  console.log(`Using PocketBase URL: ${BASE}\n`);

  console.log('Parsing migration files...\n');
  const { creates, alters } = parseMigrations();
  console.log(`\nFound ${creates.length} creates, ${alters.length} alters.\n`);

  console.log('Resolving collection IDs...');
  const idMap = await resolveCollectionIds();

  // 1. Run alter migrations first (e.g. add fields to built-in users collection)
  if (alters.length > 0) {
    console.log('Altering existing collections...\n');
    for (const alter of alters) {
      const altered = await alterCollection(alter);
      idMap[alter.name] = altered.id;
    }
    console.log('');
  }

  // 2. Create new collections (two-pass: create without unresolvable relations, then patch)
  const collectionsWithRelations = [];

  if (creates.length > 0) {
    console.log('Creating collections (pass 1: without deferred relations)...\n');
    for (const config of creates) {
      const allFields = config.fields || [];
      const relationFields = allFields.filter((f) => f.type === 'relation');
      const nonRelationFields = allFields.filter((f) => f.type !== 'relation');

      // Separate resolvable vs deferred relation fields
      const resolvableRelations = [];
      const deferredRelations = [];
      for (const rf of relationFields) {
        if (rf.collectionId && idMap[rf.collectionId]) {
          resolvableRelations.push({ ...rf, collectionId: idMap[rf.collectionId] });
        } else {
          deferredRelations.push(rf);
        }
      }

      const createConfig = { ...config, fields: [...nonRelationFields, ...resolvableRelations] };
      const resolved = replaceCollectionIds(createConfig, idMap);
      const created = await createCollection(resolved);
      idMap[config.name] = created.id;

      if (deferredRelations.length > 0) {
        collectionsWithRelations.push({ name: config.name, relationFields: deferredRelations });
      }
    }
  }

  // Pass 2: Patch deferred relation fields now that all collections exist
  if (collectionsWithRelations.length > 0) {
    console.log('\nPatching relation fields (pass 2)...\n');
    for (const { name, relationFields } of collectionsWithRelations) {
      const existing = await getCollectionByName(name);
      if (!existing) {
        console.log(`  ✗ ${name} not found for relation patch`);
        continue;
      }

      const resolvedRelations = relationFields.map((f) => {
        const realId = idMap[f.collectionId];
        return realId ? { ...f, collectionId: realId } : f;
      });

      const mergedFields = [...(existing.fields || []), ...resolvedRelations];
      const res = await fetch(`${BASE}/api/collections/${existing.id}`, {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json', ...authHeaders },
        body: JSON.stringify({ fields: mergedFields }),
      });

      if (res.ok) {
        console.log(`  ✓ ${name} patched — added relations: ${resolvedRelations.map((f) => f.name).join(', ')}`);
      } else {
        const data = await res.json();
        throw new Error(`Failed to patch relations on "${name}": ${JSON.stringify(data)}`);
      }
    }
  }

  console.log('\n✓ All migrations applied!');
}

main().catch((err) => {
  console.error('\n✗ Error:', err.message);
  process.exit(1);
});
