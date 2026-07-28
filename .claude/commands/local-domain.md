---
description: Add or inspect a local wildcard dev domain (*.something.test) for orchd
argument-hint: "[domain, e.g. rnproject.test | status | list]"
allowed-tools: Bash(dev/domain.sh:*), Bash(brew services list), Bash(curl:*), Bash(dig:*), Read
---

Set up or inspect a local wildcard domain so orchd routes by Host locally the
same way it does in production (Caddy -> gateway -> route table), instead of
port-per-workload mapping.

Argument: `$ARGUMENTS` (a base domain like `rnproject.test`, or `status` / `list`).

Do this:

1. If the argument is `status` or `list`, just run `dev/domain.sh status` and
   report it. Stop there.
2. Otherwise treat the argument as a base domain. If it's empty, ask which
   domain to add (suggest `rnproject.test`).
3. Run `dev/domain.sh status` first. If dnsmasq/Caddy aren't installed or the
   generated Caddyfile is missing, run `dev/domain.sh setup` — tell the user it
   needs sudo (ports 53/80/443, `/etc/resolver`) before running it.
4. Run `dev/domain.sh add <domain>`.
5. Verify: `dig +short admin.<domain> @127.0.0.1` returns `127.0.0.1`, and
   `curl -sS -o /dev/null -w '%{http_code}\n' https://api.<domain>/healthz`
   answers (502 is expected and fine when orchd isn't running yet).
6. Tell the user the three URLs (`admin.`, `api.`, `<key>.`) and the command to
   start the stack against it: `DOMAIN=<domain> dev/local.sh local`.

Notes: certs come from the already-trusted mkcert local CA; one dnsmasq rule
covers the whole TLD, so a second base domain under `.test` needs no DNS change.
