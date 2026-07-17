# RapidNative app (expo) runner. A real Expo project is scaffolded and its web
# target is exported at build time; the runtime image just serves the static
# export on 0.0.0.0:8080. This keeps the runtime light and fast to boot on a
# shared box. (The full Metro web dev server is heavier and is a better fit for
# the future bare-metal tier.)
FROM node:22-slim AS build
WORKDIR /build
RUN npx --yes create-expo-app@latest app --template blank >/dev/null 2>&1
WORKDIR /build/app
RUN npx expo install react-dom react-native-web @expo/metro-runtime >/dev/null 2>&1
RUN npx expo export --platform web >/dev/null 2>&1

FROM node:22-slim
WORKDIR /app
COPY --from=build /build/app/dist /app/dist

COPY <<'JS' /app/serve.mjs
import http from 'node:http'
import { readFile } from 'node:fs/promises'
import { existsSync, statSync } from 'node:fs'
import path from 'node:path'

const root = '/app/dist'
const port = Number(process.env.PORT || 8080)
const types = {
  '.html': 'text/html', '.js': 'text/javascript', '.css': 'text/css',
  '.json': 'application/json', '.png': 'image/png', '.jpg': 'image/jpeg',
  '.svg': 'image/svg+xml', '.ico': 'image/x-icon', '.map': 'application/json',
  '.ttf': 'font/ttf', '.woff2': 'font/woff2',
}

http.createServer(async (req, res) => {
  try {
    const p = decodeURIComponent(new URL(req.url, 'http://x').pathname)
    let f = path.join(root, p)
    if (p === '/' || !existsSync(f) || statSync(f).isDirectory()) {
      f = path.join(root, 'index.html') // SPA fallback
    }
    const buf = await readFile(f)
    res.setHeader('content-type', types[path.extname(f)] || 'application/octet-stream')
    res.end(buf)
  } catch {
    res.statusCode = 500
    res.end('error')
  }
}).listen(port, '0.0.0.0', () => console.log('rn-expo web on 0.0.0.0:' + port))
JS

ENV PORT=8080
EXPOSE 8080
CMD ["node", "/app/serve.mjs"]
