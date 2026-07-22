# Fullstack V2

Base template for new RapidNative fullstack projects — a monorepo with three workspaces.

```
├── mobile/              # Expo 54 + React Native 0.81 (primary)
├── api/               # Express.js API server
├── web/               # Static HTML web app
└── rapidnative.json   # Workspace manifest for the RapidNative editor
```

## Workspaces

- **mobile/** — Primary workspace. Expo Router 6 for navigation, NativeWind 4 for styling, TanStack Query 5 for data fetching, and Vibecode DB for database operations (mock + Supabase adapters). Includes PocketBase migration and seed scripts.
- **api/** — Minimal Express server with health check endpoint. Add your custom API routes here.
- **web/** — Static HTML placeholder for companion web pages.

## `rapidnative.json`

Workspace manifest consumed by the RapidNative editor to locate entry points, resolve path aliases, and run each workspace.

See each workspace's own README for detailed documentation.
