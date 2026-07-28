# dev — running the whole stack on your machine

Two ways to reach workloads locally. They differ only in how a request finds a
workload; the control plane, gateway and drivers are identical.

| | Port mode | Domain mode |
| --- | --- | --- |
| Command | `dev/local.sh local` | `DOMAIN=rnproject.test dev/local.sh local` |
| Workload URL | `http://localhost:8100`, `:8101`, … | `https://<key>.rnproject.test` |
| How it routes | one pinned host port per workload | Host header → Caddy → gateway → route table |
| TLS | no | yes (mkcert local CA) |
| API key | `local-dev-key`, pasted into the admin gate | none — open control plane, no gate |
| Matches prod | no (prod has no port mapping) | **yes**, same path as production |
| Setup needed | none | `dev/domain.sh setup` once |

Port mode is the zero-setup option. Domain mode is the one to use when you care
that local behaves like the box — Caddy in front, hostname routing, per-workload
subdomains, HTTPS.

---

## Port mode

```bash
dev/local.sh [local|docker|mock]
```

- `local` (default) — no Docker. tinbase and the RapidNative dev apps
  (web/api/mobile) run as real OS processes from a scaffolded source dir,
  `npm install`ed on first boot. Needs node/npm, plus `ORCHD_TINBASE_BIN` for
  the tinbase workload.
- `docker` — real containers under runc (no gVisor needed). Needs local images.
- `mock` — in-memory. Exercises the control plane and admin with no Docker;
  workloads don't serve traffic.

Layout: API `:8090`, gateway `:8091`, admin `:8092`, workloads from `:8100` up.
Every port is overridable (`API_PORT`, `GW_PORT`, `ADMIN_PORT`, `PORT_BASE`).

## Domain mode (production-shaped)

### One-time setup

```bash
dev/domain.sh setup
```

Installs and wires `dnsmasq` (wildcard DNS to loopback) and `caddy` (the front
door, same role as on the box), and installs the mkcert local CA. Needs sudo:
ports 53/80/443, `/etc/resolver`, root-owned brew services.

### Per base domain

```bash
dev/domain.sh add rnproject.test
```

- points the whole `.test` TLD at `127.0.0.1` via dnsmasq + `/etc/resolver/test`
  (one rule covers every base domain under it — a second domain needs no DNS work)
- issues a cert for `rnproject.test` and `*.rnproject.test` from the mkcert CA,
  so browsers trust it with no warning
- regenerates `.localdev/domains/Caddyfile` and reloads Caddy

Any `*.<name>.test` works. `.test` is reserved by RFC 6761, so it can never
collide with a real domain.

### Run it

```bash
DOMAIN=rnproject.test dev/local.sh local
```

| | |
| --- | --- |
| `https://admin.rnproject.test` | admin panel (Vite dev server behind Caddy) |
| `https://api.rnproject.test` | control-plane API |
| `https://<key>.rnproject.test` | any workload, resolved by the route table |

Workloads get **no host port** in this mode (`ORCHD_PORT_BASE=0`), exactly as in
production: the only way in is the Host header. A project's default keys are
`<ref>` for the primary workload and `<ref>-<name>` for the named ones, so a
project `abc123` from the rapidnative template lands on `abc123.rnproject.test`
(db), `abc123-api`, `abc123-web`, `abc123-mobile`.

### Admin login

Domain mode runs the control plane with **no API key**, and the admin panel
probes `/v1/projects` unauthenticated on load — a 200 means there is nothing to
gate on, so it skips the key screen entirely. Port mode keeps the key file and
the gate (`local-dev-key`).

### Other commands

```bash
dev/domain.sh list      # registered domains
dev/domain.sh status    # domains, services, generated config path, ports
dev/domain.sh reload    # re-render vhosts after changing ports — no sudo
dev/domain.sh remove rnproject.test
dev/domain.sh down      # stop caddy + dnsmasq
```

`reload` is what you want when another process already owns 8090/8091/8092:

```bash
API_PORT=8190 GW_PORT=8191 ADMIN_PORT=8192 dev/domain.sh reload
API_PORT=8190 GW_PORT=8191 ADMIN_PORT=8192 DOMAIN=rnproject.test dev/local.sh local
```

It pushes the new config through Caddy's local admin API, so no password prompt.

---

## How it maps to production

`.localdev/domains/Caddyfile` is generated as a twin of `deploy/Caddyfile`'s
`(wildcard)` snippet — same host matchers, same dispatch:

| | Local | Box |
| --- | --- | --- |
| Front door | Caddy (brew service, root) | Caddy (systemd) |
| Certs | mkcert local CA | Let's Encrypt, on-demand, gated by `/internal/tls-allow` |
| DNS | dnsmasq → 127.0.0.1 | wildcard `A` record |
| `admin.<base>` | Vite dev server | built SPA from `/opt/tinbase-cloud/admin` |
| `api.<base>` | orchd API | orchd API |
| `*.<base>` | gateway → route table | gateway → route table |
| Workload substrate | OS processes from a folder/tarball | Docker + gVisor |

The last row is the only real difference left, and it is below the gateway — the
routing, waking and scale-to-zero paths are the same code either way.

## Troubleshooting

**`domain 'x.test' is not registered`** — run `dev/domain.sh add x.test`.

**Port already in use** — something else owns 8090/8091/8092. Use the
`API_PORT=… dev/domain.sh reload` recipe above.

**`template "x": open /old/path/orchd.json: no such file`** — a template path in
the SQLite state points somewhere that no longer exists (e.g. the repo moved).
orchd now re-points such entries at `ORCHD_TEMPLATES_DIR` on startup; just
restart. To set one by hand:
`curl -X PUT $API/v1/templates -d '{"name":"rapidnative","path":"'$PWD'/template-examples/rapidnative"}'`
(the field is `path`, not `dir`).

**Routes still on `*.localhost`** — routes are minted at provision time from the
base domain in effect, so projects created in port mode keep their old hosts.
Re-provision, or wipe `.localdev/state`.

**tinbase workload `failed`** — `exec: "tinbase": executable file not found`.
Set `ORCHD_TINBASE_BIN=/path/to/tinbase`.

**Browser shows a cert warning** — the mkcert CA isn't installed; run
`mkcert -install` (or `dev/domain.sh setup` again) and restart the browser.

**DNS doesn't resolve** — `dig +short foo.rnproject.test @127.0.0.1` should say
`127.0.0.1`. If it does but the browser disagrees, the resolver file is missing:
check `/etc/resolver/test`, then `sudo killall -HUP mDNSResponder`.
