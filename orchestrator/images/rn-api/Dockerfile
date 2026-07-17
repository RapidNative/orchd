# RapidNative api-server runner: a minimal Hono server on Node.
# Listens on 0.0.0.0:$PORT (8080) so the orchestrator can publish and proxy it.
FROM node:22-slim
WORKDIR /app
RUN npm init -y >/dev/null 2>&1 \
 && npm install hono @hono/node-server >/dev/null 2>&1

COPY <<'JS' /app/server.mjs
import { Hono } from 'hono'
import { serve } from '@hono/node-server'

const app = new Hono()
app.get('/', (c) => c.json({ service: 'rapidnative-api', runtime: 'hono', ok: true }))
app.get('/health', (c) => c.text('ok'))
app.get('/api/hello', (c) => c.json({ hello: 'world', at: new Date().toISOString() }))

const port = Number(process.env.PORT || 8080)
serve({ fetch: app.fetch, hostname: '0.0.0.0', port })
console.log(`rapidnative-api (hono) listening on 0.0.0.0:${port}`)
JS

ENV PORT=8080
EXPOSE 8080
CMD ["node", "/app/server.mjs"]
