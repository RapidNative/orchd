# Expo App

## Tech Stack

Expo 54, React Native 0.81, Expo Router 6, TanStack Query 5, NativeWind 4, Vibecode DB, TypeScript strict, lucide-react-native

## Quick Reference

### File Locations

All paths relative to project root:

| Type                | Location                          | Export                        |
| ------------------- | --------------------------------- | ----------------------------- |
| Screens (protected) | `app/(app)/*.tsx`                 | default                       |
| Screens (public)    | `app/(auth)/*.tsx`                | default                       |
| Components          | `components/*.tsx`                | named → `components/index.ts` |
| Hooks               | `src/hooks/*.ts`                  | named → `src/hooks/index.ts`  |
| Providers           | `src/providers/*.tsx`             | named                         |
| Client factory      | `src/db/client.ts`               | `buildClient`, `getEnvConfig`, `isMock` |
| App context          | `src/providers/AppProvider.tsx`   | `useApp` (provides `client`)  |
| Schema              | `src/db/schema.ts`               | inline                        |
| Seed data           | `src/db/seeds/<table>.ts`        | default `SeedEntry`           |
| Seed registry       | `src/db/seeds/index.ts`          | `seeds` array                 |

### Client Architecture

The vibecode client is accessed via React context, not a global export:

- `src/db/client.ts` — async `buildClient()` factory, `getEnvConfig()` for env vars, `isMock` flag
- `src/providers/AppProvider.tsx` — `AppProvider` context, `useApp()` hook returns `{ client }`
- **In screens/hooks:** `const { client } = useApp()` then `client.from('table').select('*')`
- **For auth:** use `useAuth()` hook (wraps `client.auth` in React Query mutations)
- **Mock adapter** auto-signs-in during `buildClient()` — no manual auth needed in dev
- **Query persistence** is skipped for mock adapter (`isMock` flag in `AppProviders`)

### UI Components

Use React Native primitives with NativeWind styling:

| Category    | Components                                                        |
| ----------- | ----------------------------------------------------------------- |
| Layout      | `View`, `SafeAreaView` (from react-native-safe-area-context)      |
| Typography  | `Text`                                                            |
| Forms       | `TextInput`, `Pressable`, `TouchableOpacity`                      |
| Lists       | `FlatList`, `ScrollView`, `SectionList`                           |
| Feedback    | `ActivityIndicator`                                               |
| Images      | `Image`, `ImageBackground`                                        |
| Icons       | Import from `lucide-react-native`                                 |

### Rules

**Do:**

- Access the client via `useApp().client` — never import a global `vibecode`
- Use `useAuth()` for sign in/up/out — it handles React Query cache invalidation
- Use React Native components with NativeWind `className` for all styling
- Use semantic color classes (`bg-background`, `text-foreground`, etc.)
- Check `{ error }` from all db operations
- Use query keys: `['resource', userId]`
- Include `id`, `created_at`, `updated_at` on insert
- Export from index.ts
- Use `useCallback` for FlatList handlers
- Use `.limit(50)` for lists
- **MANDATORY: When adding or updating any database entity, you MUST update all files below in the same response — never leave them out of sync:**
  1. `src/db/schema.ts` — add/update `defineTable` export + TypeScript `type` (use `belongsTo` for relations). Tables are auto-registered via `import * as schema` in client.ts
  2. `src/db/seeds/<table>.ts` — add/update seed file with mock data (import type from `schema.ts`, export default `SeedEntry`)
  3. `src/db/seeds/index.ts` — register new seed: add import + append to `seeds` array
  - Adding a new table → do all 3 steps
  - Adding a column → update both the `defineTable` call and the TypeScript type in `schema.ts` + update the seed file's rows to include the new column
  - Renaming/dropping → update `schema.ts`, seed file, and registry to match

**Don't:**

- Import `vibecode` directly — use `useApp().client`
- Call `client.auth.*` directly in screens — use `useAuth()` hook
- Use `StyleSheet.create()`
- Hardcode colors
- Use `any` types
- Expose raw errors to users

### Banned (will crash the app or break the web preview)

- **Prisma stack** — no `prisma/schema.prisma`, no `@prisma/client`, no `PrismaClient`. The DB layer is Vibecode DB via `useApp().client`. If a Prisma-shaped solution feels natural, translate it into `defineTable` in `src/db/schema.ts` (auto-generated; do not write it by hand).
- **Native-only packages** — `react-native-webrtc`, `react-native-incall-manager`, `@react-native-firebase/*` and similar break the web preview the moment they're imported. Either use a web-supported alternative (browser `RTCPeerConnection`, `firebase` web SDK) or gate native code behind `Platform.OS` with a real web fallback.
- **Rewriting read-only files** — `src/db/client.ts`, `src/db/schema.ts`, `src/db/seeds/*`, `src/providers/AppProvider.tsx`, `src/providers/ThemeProvider.tsx`, `src/hooks/useAuth.ts`, the root `app/_layout.tsx`, and `package.json` ship complete from the scaffold. Add new code around them, never regenerate them. Never pass `value={...}` to `<ThemeProvider>` — it takes no props.
- **Hooks at module top level** — `const queryClient = useQueryClient();` outside a component crashes with "Invalid hook call". Hooks belong inside the component or another hook.
- **Hallucinated icon names** — only emit icons that actually exist in `lucide-react-native`. `MessageIcon` and `RecordIcon` do NOT exist (use `MessageCircleIcon` / `DiscIcon`). When unsure, pick the closest icon that is on the approved list.
- **Provider/consumer asymmetry** — every method called on a context (`useWebSocket().sendMessage(...)`) must be declared on the context type, included in the provider `value`, AND stubbed in the no-op fallback. Adding the consumer side alone is a runtime crash.
- **Template artifacts in source files** — never let chat-frame fragments like `` ``<CodeProject> `` or stray ` ```tsx ` markers end up inside `.ts` / `.tsx` files. The last line of every file must be valid syntax.

## Behavior

- Use TodoWrite for multi-step tasks
- Conventional commits: `type(scope): description`
- Read files before editing
- Prefer editing over creating new files
- Reference `.claude/skills/` for domain-specific patterns
