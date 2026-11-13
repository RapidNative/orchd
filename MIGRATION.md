# Migration: product-neutral engine with an adopter config

This change moves every RapidNative-specific behavior out of ORCHD's engine into
an adopter config file (`ORCHD_ADOPTER_CONFIG`). With RapidNative's file
(`deploy/adopters/rapidnative/adopter.json`) the engine behaves exactly as before.
Without it, it is neutral.

## What moved

| Was hard-coded in the engine | Now in RapidNative's adopter file |
| --- | --- |
| workload type `rapidnative-dev` on every template/preset app workload | `appWorkloadType: "rapidnative-dev"` |
| presets `expo` / `vite` / `api` (`rn-expo:dev`, `rn-vite:dev`, `rn-api:dev`, port 8080, names `app` / `web` / `api`) | `presets` |
| local process-driver recipes for those three images | `processRecipes` |
| readiness probe headers `expo-platform: ios`, `Accept: application/expo+json,application/json` | `readyProbeHeaders` |
| project env `RAPIDNATIVE_GLOBAL_SERVICES_KEY` for the SMTP relay / SMS gateway | `servicesKeyEnv` |
| mirroring `EXPO_DEV_SERVER_ORIGIN` to `EXPO_PUBLIC_DEV_SERVER_ORIGIN` | `envMirrors` |
| tinbase site URL taken from the workload named `mobile` | `siteUrlWorkload: "mobile"` |
| usage reports reading `<uuid>` / `<uuid>-prod` project names | `externalProjectIdSuffixes: ["-prod"]` |

Stored data needs no migration: workloads stored as `rapidnative-dev` keep their
type and behavior (the engine treats every type but `tinbase-project` as an app
workload).

## What does not change for RapidNative

- API responses: `type` stays `rapidnative-dev` (the website reads it in
  `device-errors.ts`), `GET /v1/presets` and `/v1/info` list the same presets.
  Verified by running the old and new binaries side by side on the mock driver
  with the website's calls (info, presets, template, create from template, create
  from presets, workload stop/start/get, delete): responses identical.
- Resource defaults, readiness timeouts, workspace mounts, env, relay settings,
  metering ids.

## Deploy (staging, then production)

The new binary **refuses to start** when `ORCHD_TINBASE_SMTP_HOST` or
`ORCHD_TINBASE_SMS_API_URL` is set and no adopter config names `servicesKeyEnv`, and
when the adopter file is missing or malformed. So the file goes first.

1. **Ship the config with the old binary still running** (the old binary ignores
   `ORCHD_ADOPTER_CONFIG`):
   - copy `deploy/adopters/rapidnative/adopter.json` to `/opt/orchd/adopter.json`;
   - install the unit with `Environment=ORCHD_ADOPTER_CONFIG=/opt/orchd/adopter.json`
     (both `deploy/orchd.service` and `deploy/orchd.service.production` have it);
   - `systemctl daemon-reload` (no restart needed yet).

   `deploy/deploy.sh` does all of this before it swaps the binary.
2. **Deploy the binary** (`deploy/deploy.sh` as usual). Check the log line
   `adopter config: /opt/orchd/adopter.json (app workloads "rapidnative-dev", 3 presets, 3 process recipes)`.
3. **Verify**: `GET /v1/presets` returns `["api","expo","tinbase","vite"]`; create a
   project from the RapidNative image the way the website does and check its
   workloads' `type`; open an existing project's phone preview (device errors
   find the mobile workload); send a password reset (SMTP relay) on a project with
   a services key.

For production, copy `deploy/orchd.service.production` to the box as before; it
carries the same `ORCHD_ADOPTER_CONFIG` line, and `adopter.json` must be at
`/opt/orchd/adopter.json` before the binary is swapped.

## Rollback

Deploy the previous binary. It ignores `ORCHD_ADOPTER_CONFIG` and the adopter file,
and reads the same stored data, so nothing else needs undoing.

## Other adopters

- **Lifo Cloud** (container boxes) runs the neutral engine. Its runner currently
  creates workloads with `type: "rapidnative-dev"`, which still works (any type
  but the database is an app); it should send `"app"`.
- **tinbase cloud** uses the `tinbase` preset only and needs no adopter file.
