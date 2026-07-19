#!/usr/bin/env bash
# Preflight checks that must pass before a deploy: Go vet + tests, and the admin
# typecheck + lint. Fast (no Docker, no network) and hermetic. deploy.sh runs
# this first and aborts on failure. Set SKIP_PREFLIGHT=1 to bypass in an
# emergency (don't make a habit of it).
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
cd "$ROOT"

if [[ "${SKIP_PREFLIGHT:-}" == "1" ]]; then
  echo "!! SKIP_PREFLIGHT=1 — skipping preflight checks"
  exit 0
fi

say(){ printf '\n-- %s\n' "$*"; }

say "go vet"
( cd orchestrator && go vet ./... )

say "go test"
( cd orchestrator && go test ./... )

say "admin typecheck"
( cd admin && npm run --silent typecheck )

say "admin lint"
( cd admin && npm run --silent lint )

# Browser E2E: boots a real orchd with the mock driver (no Docker) and drives
# the UI in Chromium. Needs the Playwright browser; SKIP_E2E=1 bypasses it.
if [[ "${SKIP_E2E:-}" == "1" ]]; then
  say "admin e2e — skipped (SKIP_E2E=1)"
else
  say "admin e2e (mock-Docker browser tests)"
  ( cd admin && npm run --silent e2e:install >/dev/null && npm run --silent e2e )
fi

echo
echo "preflight: OK"
