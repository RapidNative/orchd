/**
 * Query Client Configuration
 *
 * Designer mode: no caching, always fetch fresh data, no offline persistence.
 * Standalone mode: full caching + offline persistence using AsyncStorage.
 */

import { QueryClient } from '@tanstack/react-query'
import { createAsyncStoragePersister } from '@tanstack/query-async-storage-persister'
import AsyncStorage from '@react-native-async-storage/async-storage'

const isDesigner = process.env.EXPO_PUBLIC_ENV === 'designer'

// ─────────────────────────────────────────────────────────────────────────────
// Query Client
// ─────────────────────────────────────────────────────────────────────────────

export const queryClient = new QueryClient({
  defaultOptions: isDesigner
    ? {
        // Designer: no caching, always fetch fresh data
        queries: {
          staleTime: 0,
          gcTime: 0,
          retry: 2,
          retryDelay: (attemptIndex) =>
            Math.min(1000 * 2 ** attemptIndex, 30000),
          refetchOnWindowFocus: false,
          refetchOnReconnect: true,
          networkMode: 'always',
        },
        mutations: {
          retry: false,
          networkMode: 'always',
        },
      }
    : {
        // Standalone: full caching + offline persistence
        queries: {
          staleTime: 1000 * 60 * 5, // 5 minutes
          gcTime: 1000 * 60 * 60 * 24, // 24 hours (must be >= persister maxAge)
          retry: 2,
          retryDelay: (attemptIndex) =>
            Math.min(1000 * 2 ** attemptIndex, 30000),
          refetchOnWindowFocus: false,
          refetchOnReconnect: true,
          networkMode: 'offlineFirst',
        },
        mutations: {
          retry: false,
          networkMode: 'offlineFirst',
        },
      },
})

// ─────────────────────────────────────────────────────────────────────────────
// AsyncStorage Persister
// ─────────────────────────────────────────────────────────────────────────────

export const asyncStoragePersister = createAsyncStoragePersister({
  storage: AsyncStorage,
  // Key used to store the cache
  key: 'REACT_QUERY_OFFLINE_CACHE',
  // Throttle writes to storage (prevents excessive writes)
  throttleTime: 1000,
  // Optional: serialize/deserialize functions
  serialize: (data) => JSON.stringify(data),
  deserialize: (data) => JSON.parse(data),
})

// ─────────────────────────────────────────────────────────────────────────────
// Persist Options
// ─────────────────────────────────────────────────────────────────────────────

export const persistOptions = {
  persister: asyncStoragePersister,
  // Maximum age of persisted data (24 hours)
  maxAge: 1000 * 60 * 60 * 24,
  // Only persist successful queries
  dehydrateOptions: {
    shouldDehydrateQuery: (query: any) => {
      // Don't persist queries with errors
      if (query.state.status === 'error') return false
      // Don't persist auth queries (security)
      if (query.queryKey[0] === 'auth') return false
      return true
    },
  },
}