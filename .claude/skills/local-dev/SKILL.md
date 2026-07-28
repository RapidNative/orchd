---
name: local-dev
description: Set up and run the orchd stack locally — port mode or a production-shaped wildcard domain (*.something.test) with Caddy, dnsmasq and trusted TLS. Use when asked to run the stack, start orchd/admin locally, add or fix a local dev domain, provision a test project, or debug local routing, DNS, certs, ports or templates.
---

# Local dev environment

Full reference: `dev/README.md`. This skill is the operating procedure.

## Pick a mode first

- **Port mode** — `dev/local.sh [local|docker|mock]`. Zero setup. Workloads on
  `http://localhost:8100+`. Admin gate wants the key `local-dev-key`.
- **Domain mode** — `DOMAIN=<base> dev/local.sh local`. Matches production:
  Caddy in front, Host-based routing through the gateway's route table, real
  TLS, no host ports, no API key, and scale-to-zero on (`IDLE_TIMEOUT`, default
  5m; set `0` to keep everything warm, `20s` to watch it work). Use this
  whenever the task touches routing, subdomains, TLS, the gateway,
  scale-to-zero, or "behaves like prod".

Default to domain mode when the user names a domain; otherwise ask only if the
distinction changes the outcome.

## Bring up domain mode

```bash
dev/domain.sh status                 # what already exists
dev/domain.sh setup                  # once per machine — NEEDS SUDO
dev/domain.sh add rnproject.test     # per base domain — NEEDS SUDO
DOMAIN=rnproject.test dev/local.sh local
```

**You cannot type a sudo password.** When a step needs one, ask the user to run
it with the `! ` prefix so the output lands in the session, then continue.
`dev/domain.sh reload` does *not* need sudo — it pushes config through Caddy's
admin API — so prefer it for anything after the initial setup.

Before starting, check the ports are free:

```bash
lsof -nP -iTCP:8090 -iTCP:8091 -iTCP:8092 -sTCP:LISTEN
```

If taken, do not kill processes that belong to unrelated projects. Shift ports
instead, and reload Caddy to match:

```bash
API_PORT=8190 GW_PORT=8191 ADMIN_PORT=8192 dev/domain.sh reload
API_PORT=8190 GW_PORT=8191 ADMIN_PORT=8192 DOMAIN=rnproject.test dev/local.sh local
```

Start the stack in the background with output to a log
(`> .localdev/stack.log 2>&1 &`) so you can keep working, and tail the log rather
than blocking.

## Verify (always do this, don't just report "started")

```bash
curl -s -o /dev/null -w '%{http_code}\n' https://api.rnproject.test/healthz
curl -s -o /dev/null -w '%{http_code}\n' https://admin.rnproject.test/
curl -s -X POST https://api.rnproject.test/v1/projects \
  -H 'content-type: application/json' -d '{"name":"demo","template":"rapidnative"}'
```

Then poll `GET /v1/projects/<id>` until the workloads leave `provisioning`, and
curl at least one workload endpoint to prove routing works end to end. First
boot runs `npm install` per app — expect a couple of minutes, and don't call a
workload broken until its install has finished.

## Known failure modes

| Symptom | Cause / fix |
| --- | --- |
| `domain 'x' is not registered` | `dev/domain.sh add x` |
| `template "x": open /old/path/orchd.json` | stale path in `.localdev/state/orchd.db`; orchd self-heals on restart against `ORCHD_TEMPLATES_DIR`, or `PUT /v1/templates` with `{"name","path"}` (field is `path`) |
| workload `failed`, `exec: "tinbase"` | set `ORCHD_TINBASE_BIN` |
| workload `failed` right after install | read `.localdev/stack.log`, then run `npm install` by hand in `.localdev/projects/<proj>/<wl>/<app>` to see the real npm error |
| workload `suspended` | expected in domain mode after the idle timeout — one request wakes it |
| routes still `*.localhost` | provisioned in port mode; re-provision or wipe `.localdev/state` |
| cert warning | `mkcert -install`, restart browser |
| DNS not resolving | `dig +short x.rnproject.test @127.0.0.1`; check `/etc/resolver/<tld>`; `sudo killall -HUP mDNSResponder` |

## Ground rules

- The repo is the source of truth. Fix the template or the script, not just the
  materialized copy under `.localdev/projects/`.
- `.localdev/` is disposable state, gitignored — safe to wipe, never commit.
- Leave the stack running when you're done unless asked otherwise, and tell the
  user the URLs and how to stop it (`Ctrl-C`; `dev/domain.sh down` for the
  services).
