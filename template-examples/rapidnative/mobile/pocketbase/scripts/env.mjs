/**
 * Shared env loader and auth resolver for PocketBase scripts.
 *
 * Authenticates as superuser using POCKETBASE_SERVICE_ROLE_KEY (the admin key).
 * PocketBase API rules handle authorization for record operations.
 *
 * Priority: env vars > interactive prompt
 */

import { readFileSync } from 'fs';
import { createInterface } from 'readline';
import { resolve, dirname } from 'path';
import { fileURLToPath } from 'url';

const __dirname = dirname(fileURLToPath(import.meta.url));

function prompt(question) {
  const rl = createInterface({ input: process.stdin, output: process.stdout });
  return new Promise((res) =>
    rl.question(question, (answer) => {
      rl.close();
      res(answer.trim());
    })
  );
}

function loadEnv() {
  try {
    const envPath = resolve(__dirname, '../../.env');
    const content = readFileSync(envPath, 'utf-8');
    const vars = {};
    for (const line of content.split('\n')) {
      const trimmed = line.trim();
      if (!trimmed || trimmed.startsWith('#')) continue;
      const eqIdx = trimmed.indexOf('=');
      if (eqIdx === -1) continue;
      vars[trimmed.slice(0, eqIdx)] = trimmed.slice(eqIdx + 1);
    }
    return vars;
  } catch {
    return {};
  }
}

async function authenticateSuperuser(base, { identity, password }) {
  const res = await fetch(`${base}/api/collections/_superusers/auth-with-password`, {
    method: 'POST',
    headers: { 'Content-Type': 'application/json' },
    body: JSON.stringify({ identity, password }),
  });
  const data = await res.json();
  if (!res.ok) throw new Error(`Superuser auth failed: ${JSON.stringify(data)}`);
  return data.token;
}

/**
 * Resolves PocketBase URL and auth headers.
 * Authenticates as superuser using the service role key.
 *
 * @returns {{ base: string, authHeaders: Record<string, string> }}
 */
export async function resolveEnv() {
  const env = loadEnv();

  let base = env.EXPO_PUBLIC_POCKETBASE_URL;
  if (!base) base = await prompt('PocketBase URL: ');
  if (!base) {
    console.error('✗ PocketBase URL is required.');
    process.exit(1);
  }

  console.log('Authenticating as superuser...\n');

  let identity, password;

  const adminKey = env.POCKETBASE_SERVICE_ROLE_KEY;
  const adminEmail = env.POCKETBASE_ADMIN_EMAIL;
  const adminPassword = env.POCKETBASE_ADMIN_PASSWORD;

  if (adminEmail && adminPassword) {
    // Use username/password auth
    identity = adminEmail;
    password = adminPassword;
  } else if (adminKey) {
    // Fall back to service role key auth
    identity = 'admin@rapidnative.com';
    password = adminKey;
  } else {
    // Interactive prompt
    const method = await prompt('Auth method — (1) Service Role Key  (2) Username & Password: ');
    if (method === '2') {
      identity = await prompt('Admin email/username: ');
      password = await prompt('Admin password: ');
    } else {
      identity = 'admin@rapidnative.com';
      password = await prompt('PocketBase Service Role Key: ');
    }
  }

  if (!identity || !password) {
    console.error('✗ Superuser credentials are required.');
    process.exit(1);
  }

  const token = await authenticateSuperuser(base, { identity, password });
  return {
    base,
    authHeaders: { Authorization: `Bearer ${token}` },
  };
}
