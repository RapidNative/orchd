/**
 * Database Client
 *
 * Async factory — adapter is chosen via EXPO_PUBLIC_ADAPTER_TYPE env var.
 * Defaults to 'mock' when not set.
 *
 * Usage: const client = await buildClient(getEnvConfig())
 */

import { createClient } from '@vibecode-db/client';
import AsyncStorage from '@react-native-async-storage/async-storage';
import { seeds } from './seeds';
import * as schema from './schema';

export const DEMO_USER_ID = 'user-1';

export type AdapterType = 'mock' | 'supabase';

export interface AdapterConfig {
  type: AdapterType;
  supabaseUrl?: string;
  supabaseKey?: string;
}

/** Read adapter config from EXPO_PUBLIC_ env variables */
export function getEnvConfig(): AdapterConfig {
  const type = (process.env.EXPO_PUBLIC_ADAPTER_TYPE as AdapterType) || 'mock';
  return {
    type,
    supabaseUrl: process.env.EXPO_PUBLIC_SUPABASE_URL || undefined,
    supabaseKey: process.env.EXPO_PUBLIC_SUPABASE_KEY || undefined,
  };
}

const isDesigner = process.env.EXPO_PUBLIC_ENV === 'designer';

async function createSeededMockClient() {
  const { MockAdapter } = require('@vibecode-db/client/adapters/mock');
  const adapter = new MockAdapter({ persistSession: true, storage: AsyncStorage });

  adapter.setSchema(...Object.values(schema));

  for (const entry of seeds) {
    adapter.seed(entry.table, entry.rows);
  }

  adapter.seedUsers([
    { id: DEMO_USER_ID, name: 'Demo User', email: 'demo@example.com', password: 'password123' },
  ]);

  // Designer: disable auth by default so sign-in screens are locked
  // PWA / Production: auth works normally
  const client = createClient('mock://localhost', 'mock-key', {
    adapter,
    config: isDesigner ? { authDisabled: true } : undefined,
  });
   if(isDesigner){
  // Auto sign-in — uses overrideAuthDisabled to bypass authDisabled in designer
    await client.auth.signInWithPassword(
      { email: 'demo@example.com', password: 'password123' },
      { overrideAuthDisabled: true },
    );
  }

  // Expose client on window for dev debugging: window.vibecode
  if (__DEV__ && typeof window !== 'undefined') {
    (window as any).vibecode = client;
  }

  return client;
}


export async function buildClient(config: AdapterConfig) {
  // Return cached client if available (survives HMR remounts)
  if (typeof window !== 'undefined' && (window as any).__VIBECODE_CLIENT__) {
    return (window as any).__VIBECODE_CLIENT__ as ReturnType<typeof createClient>;
  }

  if (config.type === 'supabase') {
    const { SupabaseAdapter } = require('@vibecode-db/client/adapters/supabase');
    const adapter = new SupabaseAdapter({
      supabaseUrl: config.supabaseUrl!,
      supabaseKey: config.supabaseKey!,
    });
    await adapter.ready;
    const client = createClient(config.supabaseUrl!, config.supabaseKey!, { adapter });
    if (typeof window !== 'undefined') (window as any).__VIBECODE_CLIENT__ = client;
    return client;
  }

  const client = await createSeededMockClient();
  if (typeof window !== 'undefined') (window as any).__VIBECODE_CLIENT__ = client;
  return client;
}
