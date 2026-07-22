/**
 * App Provider
 *
 * Initializes the vibecode client asynchronously and provides it via context.
 * All child components can access the client via useApp().
 */

import { createContext, useContext, useState, useEffect, type ReactNode } from 'react';
import { View, ActivityIndicator } from 'react-native';
import { createClient } from '@vibecode-db/client';
import { buildClient, getEnvConfig } from '@/src/db/client';

type Client = ReturnType<typeof createClient>;

interface AppContextValue {
  client: Client;
}

const AppContext = createContext<AppContextValue | null>(null);

export function useApp() {
  const ctx = useContext(AppContext);
  if (!ctx) throw new Error('useApp must be used within AppProvider');
  return ctx;
}

export function AppProvider({ children }: { children: ReactNode }) {
  const [client, setClient] = useState<Client | null>(
    () => (window as any).__VIBECODE_CLIENT__ || null
  );

  useEffect(() => {
    if (client) return;
    buildClient(getEnvConfig())
      .then((c) => setClient(c))
      .catch((err) => console.error('[AppProvider] buildClient failed:', err));
  }, []);

  if (!client) {
    return (
      <View className="flex-1 bg-background items-center justify-center">
        <ActivityIndicator size="large" className="text-primary" />
      </View>
    );
  }

  return (
    <AppContext.Provider value={{ client }}>
      {children}
    </AppContext.Provider>
  );
}
