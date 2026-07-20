import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
import tailwindcss from '@tailwindcss/vite'
import path from 'node:path'

// The dev/preview server proxies '/api' to a control plane. Defaults to the box;
// local mode (dev/local.sh) and E2E point it at a local orchd via
// API_PROXY_TARGET / E2E_API_TARGET. In prod the app is served same-origin
// behind Caddy, so '/api' is used directly (no proxy).
const apiTarget =
  process.env.API_PROXY_TARGET ?? process.env.E2E_API_TARGET ?? 'https://api.tinbase.dev'
const proxy = {
  '/api': {
    target: apiTarget,
    changeOrigin: true,
    secure: apiTarget.startsWith('https'),
    rewrite: (p: string) => p.replace(/^\/api/, ''),
  },
}

export default defineConfig({
  plugins: [react(), tailwindcss()],
  resolve: {
    alias: { '@': path.resolve(import.meta.dirname, 'src') },
  },
  server: { proxy },
  preview: { proxy },
})
