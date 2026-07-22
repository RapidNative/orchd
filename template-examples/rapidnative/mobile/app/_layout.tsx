import { Stack } from 'expo-router';
import { StatusBar } from 'expo-status-bar';
import { SafeAreaProvider } from 'react-native-safe-area-context';
import { useOffline } from '../src/hooks';
import '@/global.css';
import { type ReactNode } from 'react'
import { QueryClientProvider } from '@tanstack/react-query'
import { PersistQueryClientProvider } from '@tanstack/react-query-persist-client'
import { queryClient, persistOptions } from '@/src/lib/queryClient'
import { ThemeProvider } from '@/src/providers/ThemeProvider'
import { AppProvider } from '@/src/providers/AppProvider'


function RootLayoutNav() {
  // Initialize offline monitoring
  useOffline();

  return (
    <>
      <StatusBar style="auto" />
      <Stack screenOptions={{ headerShown: false }}>
        <Stack.Screen name="(app)" />
      </Stack>
    </>
  );
}

const isDesigner = process.env.EXPO_PUBLIC_ENV === 'designer';

function QueryProvider({ children }: { children: ReactNode }) {
  if (isDesigner) {
    return <QueryClientProvider client={queryClient}>{children}</QueryClientProvider>
  }
  return (
    <PersistQueryClientProvider client={queryClient} persistOptions={persistOptions}>
      {children}
    </PersistQueryClientProvider>
  )
}
export default function RootLayout() {
  return (
    <SafeAreaProvider>
      <ThemeProvider>
        <QueryProvider>
          <AppProvider>
            <RootLayoutNav />
          </AppProvider>
        </QueryProvider>
      </ThemeProvider>
    </SafeAreaProvider>
  );
}
