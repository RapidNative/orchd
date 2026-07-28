#!/usr/bin/env bash
# Local wildcard domains for orchd — the dev twin of deploy/Caddyfile.
#
#   dev/domain.sh setup                 install + wire dnsmasq and Caddy (once)
#   dev/domain.sh add rnproject.test    register a base domain (cert + vhost)
#   dev/domain.sh remove rnproject.test
#   dev/domain.sh list
#   dev/domain.sh reload                re-render the vhosts (e.g. after changing
#                                       API_PORT/GW_PORT/ADMIN_PORT) — no sudo
#   dev/domain.sh status
#   dev/domain.sh down                  stop the local Caddy/dnsmasq services
#
# What it gives you, per registered base domain <d>:
#
#   https://admin.<d>          admin panel   -> Vite   127.0.0.1:$ADMIN_PORT
#   https://api.<d>            control API   -> orchd  127.0.0.1:$API_PORT
#   https://<anything>.<d>     workload host  -> gateway 127.0.0.1:$GW_PORT
#
# which is exactly the prod routing shape: the gateway resolves the Host header
# against the route table, so a workload's default host <key>.<d> just works and
# no port mapping is involved. Run the stack with:
#
#   DOMAIN=rnproject.test dev/local.sh local
#
# Only `setup`, `add`, `remove` and `down` need sudo (ports 80/443, /etc/resolver,
# brew services as root). Certs come from mkcert's already-trusted local CA.
set -euo pipefail

ROOT="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
STATE="$ROOT/.localdev/domains"
LIST="$STATE/domains.txt"
CERTS="$STATE/certs"
CADDYFILE="$STATE/Caddyfile"

API_PORT="${API_PORT:-8090}"
GW_PORT="${GW_PORT:-8091}"
ADMIN_PORT="${ADMIN_PORT:-8092}"

BREW_PREFIX="$(brew --prefix)"
DNSMASQ_CONF="$BREW_PREFIX/etc/dnsmasq.conf"
DNSMASQ_D="$BREW_PREFIX/etc/dnsmasq.d"
BREW_CADDYFILE="$BREW_PREFIX/etc/Caddyfile"

say(){ printf '\n\033[1;32m==> %s\033[0m\n' "$*"; }
warn(){ printf '\033[1;33m!  %s\033[0m\n' "$*"; }
die(){ printf '\033[1;31mx  %s\033[0m\n' "$*" >&2; exit 1; }

mkdir -p "$STATE" "$CERTS"
touch "$LIST"

# tld_of rnproject.test -> test
tld_of(){ printf '%s' "${1##*.}"; }

need(){
  command -v "$1" >/dev/null 2>&1 && return 0
  say "install $1"
  brew install "$1"
}

# ---------------------------------------------------------------- setup

cmd_setup(){
  command -v brew >/dev/null || die "Homebrew required"
  need dnsmasq
  need caddy
  need mkcert

  # dnsmasq must read its conf.d dir; Homebrew ships this line but not always.
  if ! grep -qs '^conf-dir=' "$DNSMASQ_CONF" 2>/dev/null; then
    say "enable conf-dir in $DNSMASQ_CONF"
    printf '\nconf-dir=%s/,*.conf\n' "$DNSMASQ_D" | sudo tee -a "$DNSMASQ_CONF" >/dev/null
  fi
  sudo mkdir -p "$DNSMASQ_D"

  # Caddy's brew service reads $BREW_PREFIX/etc/Caddyfile; keep that a one-liner
  # that imports our generated, repo-local config.
  if [ -f "$BREW_CADDYFILE" ] && ! grep -qs "$CADDYFILE" "$BREW_CADDYFILE"; then
    warn "backing up existing $BREW_CADDYFILE to $BREW_CADDYFILE.orchd-bak"
    sudo cp "$BREW_CADDYFILE" "$BREW_CADDYFILE.orchd-bak"
  fi
  printf 'import %s\n' "$CADDYFILE" | sudo tee "$BREW_CADDYFILE" >/dev/null

  mkcert -install
  regen
  say "setup done — now: dev/domain.sh add rnproject.test"
}

# ---------------------------------------------------------------- add / remove

cmd_add(){
  local d="${1:-}"
  [ -n "$d" ] || die "usage: dev/domain.sh add <base-domain>   (e.g. rnproject.test)"
  case "$d" in *.*) ;; *) die "need a dotted domain like rnproject.test" ;; esac
  command -v caddy >/dev/null || die "run 'dev/domain.sh setup' first"

  if grep -qxF "$d" "$LIST"; then
    warn "$d already registered — refreshing"
  else
    printf '%s\n' "$d" >> "$LIST"
  fi

  # Resolve the whole TLD (*.test) to loopback: one rule covers every base
  # domain under it, so adding the next one needs no DNS change.
  local tld; tld="$(tld_of "$d")"
  local conf="$DNSMASQ_D/orchd-$tld.conf"
  if [ ! -f "$conf" ]; then
    say "dnsmasq: *.$tld -> 127.0.0.1"
    printf 'address=/%s/127.0.0.1\n' "$tld" | sudo tee "$conf" >/dev/null
  fi
  if [ ! -f "/etc/resolver/$tld" ]; then
    say "resolver: /etc/resolver/$tld"
    sudo mkdir -p /etc/resolver
    printf 'nameserver 127.0.0.1\n' | sudo tee "/etc/resolver/$tld" >/dev/null
  fi

  say "cert for $d and *.$d (mkcert local CA)"
  mkcert -cert-file "$CERTS/$d.pem" -key-file "$CERTS/$d-key.pem" "$d" "*.$d" >/dev/null

  regen
  restart_services

  cat <<EOF

  ┌──────────────────────────────────────────────────────────────┐
  │  $d is live
  ├──────────────────────────────────────────────────────────────┤
  │  Admin      https://admin.$d
  │  API        https://api.$d
  │  Workloads  https://<key>.$d      (gateway, Host-routed)
  └──────────────────────────────────────────────────────────────┘

  Start the stack bound to it:

      DOMAIN=$d dev/local.sh local

EOF
}

cmd_remove(){
  local d="${1:-}"
  [ -n "$d" ] || die "usage: dev/domain.sh remove <base-domain>"
  grep -vxF "$d" "$LIST" > "$LIST.tmp" || true
  mv "$LIST.tmp" "$LIST"
  rm -f "$CERTS/$d.pem" "$CERTS/$d-key.pem"
  regen
  restart_services
  say "removed $d (DNS for *.$(tld_of "$d") left in place)"
}

cmd_list(){
  # NB: bash locals are dynamically scoped — loop vars here and in regen must
  # not be named `d`, or they clobber the caller's domain.
  local base
  if [ ! -s "$LIST" ]; then echo "(no domains registered)"; return; fi
  while read -r base; do
    [ -n "$base" ] || continue
    printf '  %-24s admin.%s  api.%s  *.%s\n' "$base" "$base" "$base" "$base"
  done < "$LIST"
}

cmd_status(){
  echo "domains:"; cmd_list
  echo
  echo "services:"
  brew services list 2>/dev/null | grep -E '^(caddy|dnsmasq)\b' || echo "  (none running)"
  echo
  echo "config:  $CADDYFILE"
  echo "ports:   api $API_PORT · gateway $GW_PORT · admin $ADMIN_PORT"
}

cmd_down(){
  sudo brew services stop caddy    2>/dev/null || true
  sudo brew services stop dnsmasq  2>/dev/null || true
  say "stopped caddy + dnsmasq"
}

# ---------------------------------------------------------------- generation

# regen writes the Caddyfile from the domain list. One vhost block per base
# domain, mirroring the (wildcard) snippet in deploy/Caddyfile — admin and api
# get their own hosts, everything else is a workload on the gateway.
regen(){
  local base
  {
    cat <<EOF
# GENERATED by dev/domain.sh — do not edit; edit the script or re-run 'add'.
# Local twin of deploy/Caddyfile's (wildcard) snippet: admin/api hosts dispatch
# to the dev servers, every other subdomain is a workload on the gateway.
{
	auto_https disable_redirects
}

(orchd) {
	@admin host admin.{args[0]}
	@api host api.{args[0]}

	handle @admin {
		reverse_proxy 127.0.0.1:$ADMIN_PORT
	}
	handle @api {
		reverse_proxy 127.0.0.1:$API_PORT
	}
	handle {
		reverse_proxy 127.0.0.1:$GW_PORT
	}
}
EOF
    while read -r base; do
      [ -n "$base" ] || continue
      cat <<EOF

http://$base, http://*.$base {
	import orchd $base
}

https://$base, https://*.$base {
	tls $CERTS/$base.pem $CERTS/$base-key.pem
	import orchd $base
}
EOF
    done < "$LIST"
  } > "$CADDYFILE"

  if command -v caddy >/dev/null && [ -s "$LIST" ]; then
    caddy fmt --overwrite "$CADDYFILE" >/dev/null 2>&1 || true
    caddy validate --config "$CADDYFILE" >/dev/null 2>&1 || die "generated Caddyfile is invalid: $CADDYFILE"
  fi
}

# reload pushes the generated config into a running Caddy through its local
# admin API — no sudo, unlike restarting the root-owned service.
cmd_reload(){
  regen
  caddy reload --config "$CADDYFILE" 2>/dev/null || die "caddy not running — use: dev/domain.sh add <domain>"
  say "caddy reloaded (api $API_PORT · gateway $GW_PORT · admin $ADMIN_PORT)"
}

restart_services(){
  # A running Caddy can take the new config over its admin API; only fall back
  # to the root service restart when that fails (first run, or Caddy is down).
  if caddy reload --config "$CADDYFILE" 2>/dev/null; then
    say "reloaded caddy; restart dnsmasq (root: port 53)"
    sudo brew services restart dnsmasq >/dev/null
    sudo dscacheutil -flushcache 2>/dev/null || true
    sudo killall -HUP mDNSResponder 2>/dev/null || true
    return
  fi
  say "restart dnsmasq + caddy (root: ports 53/80/443)"
  sudo brew services restart dnsmasq >/dev/null
  sudo brew services restart caddy   >/dev/null
  sudo dscacheutil -flushcache 2>/dev/null || true
  sudo killall -HUP mDNSResponder 2>/dev/null || true
}

case "${1:-}" in
  setup)  shift; cmd_setup "$@" ;;
  add)    shift; cmd_add "$@" ;;
  remove) shift; cmd_remove "$@" ;;
  list)   shift; cmd_list "$@" ;;
  status) shift; cmd_status "$@" ;;
  reload) shift; cmd_reload "$@" ;;
  down)   shift; cmd_down "$@" ;;
  *) sed -n '2,30p' "${BASH_SOURCE[0]}" | sed 's/^# \{0,1\}//'; exit 1 ;;
esac
