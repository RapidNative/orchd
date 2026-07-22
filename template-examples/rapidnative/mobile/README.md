# Base Template Example

A fully working example app built on the RapidNative fullstack template. Use this as a reference for how screens, components, database schema, seeds, and navigation come together in a real app.

> **Looking for a clean starting point?** See the sibling `base-template/` folder, which contains the same infrastructure with no app screens or seed data.

## What's in This Example

This example ships a **Todo / Task Manager** app with:

- **Home screen** — Create, toggle, filter, and delete todos
- **Profile screen** — User stats, appearance settings, sign-out
- **Auth flow** — Sign in / sign up screens with theme toggle
- **Tab navigation** — Bottom tabs (Tasks + Profile)
- **Seed data** — Demo users and todos pre-loaded via MockAdapter

## Tech Stack

- **Expo 54** with React Native 0.81
- **Expo Router 6** for file-based routing
- **TanStack Query 5** for state management and offline persistence
- **NativeWind 4** for Tailwind CSS styling
- **Vibecode DB** for database operations (Mock + Supabase adapters)
- **TypeScript** in strict mode
- **lucide-react-native** for icons

## Getting Started

### Prerequisites

- Node.js 20+
- pnpm 9.0.0+
- Expo CLI

### Installation

From the monorepo root:

```bash
pnpm install
```

### Development

```bash
# Start the Expo development server
pnpm dev:mobile

# Or from this directory
pnpm start
```

### Running on Devices

- Press `i` for iOS Simulator
- Press `a` for Android Emulator
- Scan QR code with Expo Go app

## Project Structure

```
base-template-example/
├── app/                          # Expo Router pages
│   ├── _layout.tsx               # Root layout (auth guard via useAuth, providers)
│   ├── (auth)/                   # Public auth screens
│   │   ├── _layout.tsx
│   │   ├── signin.tsx
│   │   └── signup.tsx
│   └── (app)/                    # Protected app screens
│       ├── _layout.tsx           # Tab navigation (Tasks + Profile)
│       ├── index.tsx             # Todo list with CRUD
│       └── profile.tsx           # User profile & settings
├── components/                   # Reusable UI components
│   ├── ThemeProvider.tsx         # Theme context wrapper
│   ├── ThemeToggle.tsx           # Dark/light mode toggle
│   ├── ThemedView.tsx            # Themed root view
│   └── index.ts                  # Barrel exports
├── src/
│   ├── db/
│   │   ├── client.ts             # Async client factory (buildClient, getEnvConfig, isMock)
│   │   ├── schema.ts             # defineTable exports + TypeScript types
│   │   └── seeds/                # Mock data
│   │       ├── index.ts          # Seed registry
│   │       ├── users.ts          # Demo users
│   │       └── todos.ts          # Sample todos
│   ├── hooks/                    # Custom hooks (useAuth, useOffline, useApp)
│   ├── lib/                      # Utilities (queryClient)
│   └── providers/                # Context providers
│       ├── AppProvider.tsx       # Vibecode client context (useApp hook)
│       ├── AppProviders.tsx      # Combined provider tree
│       └── ThemeProvider.tsx     # Theme context
├── .claude/                      # Claude AI configuration
│   ├── commands/                 # Slash commands (new-screen, new-table, etc.)
│   ├── docs/                     # API reference & architecture docs
│   ├── skills/                   # Domain-specific pattern guides
│   └── agents/                   # Agent definitions
├── theme.ts                      # Color tokens (light + dark themes)
├── global.css                    # Tailwind base styles
├── tailwind.config.js            # Tailwind + NativeWind config
└── package.json                  # Dependencies & scripts
```

## Architecture

### Client Access Pattern

The vibecode client is provided via React context — no global imports:

```tsx
// In any component/screen:
import { useApp } from '../src/hooks';

const { client } = useApp();
const { data } = await client.from('todos').select('*');
```

### Auth Pattern

Auth is managed via the `useAuth()` hook which wraps client auth in React Query mutations:

```tsx
const { user, isAuthenticated, signIn, signUp, signOut } = useAuth();

// Sign in
signIn.mutate({ email, password }, {
  onSuccess: () => router.replace('/'),
});
```

### Provider Tree

```
SafeAreaProvider
  └── ThemeProvider (NativeWind themes)
      └── QueryProvider (TanStack Query — persistence only for non-mock)
          └── AppProvider (vibecode client context)
              └── App routes
```

## Key Patterns Demonstrated

### Database Entity (3-file sync)

Every table requires these three files kept in sync:

1. `src/db/schema.ts` — `defineTable` export + TypeScript type (source of truth)
2. `src/db/seeds/<table>.ts` — Mock data with `SeedEntry` export
3. `src/db/seeds/index.ts` — Register the seed in the `seeds` array

### Screen Template

Screens use `SafeAreaView`, `useApp()`/`useAuth()`/`useTheme()` hooks, TanStack Query for data, and NativeWind for styling. See `app/(app)/index.tsx` for a full CRUD example.

### Theming

Colors are defined as RGB values in `theme.ts` and consumed via semantic Tailwind classes (`bg-background`, `text-foreground`, `bg-primary`, etc.). See `theme.md` for the full guide.

## Configuration

Copy `.env.example` to `.env` and configure:

```env
EXPO_PUBLIC_ADAPTER_TYPE=mock          # 'mock' or 'supabase'
EXPO_PUBLIC_SUPABASE_URL=              # Required for supabase adapter
EXPO_PUBLIC_SUPABASE_KEY=              # Required for supabase adapter
```

## Claude Commands

This project includes Claude slash commands for common tasks:

| Command          | Description                    |
| ---------------- | ------------------------------ |
| `/new-screen`    | Scaffold a new screen          |
| `/new-component` | Scaffold a new component       |
| `/new-table`     | Add a new database table       |
| `/refactor`      | Refactor code to conventions   |
| `/review`        | Review code for issues         |
