import { StrictMode } from 'react'
import { createRoot } from 'react-dom/client'
import { RouterProvider } from '@tanstack/react-router'
import { QueryCache, QueryClient, QueryClientProvider } from '@tanstack/react-query'
import { router } from './router'
import { auth } from './lib/auth'
import { ApiError } from './lib/api'
import './index.css'

const queryClient = new QueryClient({
  queryCache: new QueryCache({
    onError: (err) => {
      // A 401 anywhere means the key went bad; drop it and show the gate.
      if (err instanceof ApiError && err.status === 401) auth.clear()
    },
  }),
  defaultOptions: {
    queries: { retry: false, refetchOnWindowFocus: false },
  },
})

createRoot(document.getElementById('root')!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <RouterProvider router={router} />
    </QueryClientProvider>
  </StrictMode>,
)
