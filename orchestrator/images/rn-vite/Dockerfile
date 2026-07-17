# RapidNative web (react) runner: a Vite dev server.
# Listens on 0.0.0.0:8080; allowedHosts is open so it serves behind the gateway
# proxy (which forwards an arbitrary Host). base:'./' keeps assets relative,
# which helps while we route via subpaths instead of subdomains.
FROM node:22-slim
WORKDIR /app

COPY <<'JSON' /app/package.json
{
  "name": "rn-vite",
  "private": true,
  "type": "module",
  "scripts": { "dev": "vite" },
  "dependencies": { "react": "^18.3.1", "react-dom": "^18.3.1" },
  "devDependencies": { "vite": "^6.0.7", "@vitejs/plugin-react": "^4.3.4" }
}
JSON

COPY <<'JS' /app/vite.config.js
import { defineConfig } from 'vite'
import react from '@vitejs/plugin-react'
export default defineConfig({
  base: './',
  plugins: [react()],
  server: { host: '0.0.0.0', port: 8080, allowedHosts: true },
})
JS

COPY <<'HTML' /app/index.html
<!doctype html>
<html><head><meta charset="utf-8"><meta name="viewport" content="width=device-width,initial-scale=1"><title>RapidNative · Vite</title></head>
<body><div id="root"></div><script type="module" src="/src/main.jsx"></script></body></html>
HTML

RUN mkdir -p /app/src
COPY <<'JSX' /app/src/main.jsx
import React from 'react'
import { createRoot } from 'react-dom/client'
function App() {
  return React.createElement('div', { style: { fontFamily: 'system-ui', padding: 40 } },
    React.createElement('h1', null, 'RapidNative · Vite + React'),
    React.createElement('p', null, 'Dev server running under gVisor, routed by the orchestrator.'))
}
createRoot(document.getElementById('root')).render(React.createElement(App))
JSX

RUN npm install >/dev/null 2>&1
ENV PORT=8080
EXPOSE 8080
CMD ["npm", "run", "dev"]
