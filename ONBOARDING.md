# Welcome to ORCHD

## How We Use Claude

Based on sanketsahu's usage over the last 30 days:

Work Type Breakdown:
  Build Feature  ████████████████████  100%

  _(1 session in the window — a thin sample. Expect this to broaden as the team
  uses Claude Code more.)_

Top Skills & Commands:
  _None recorded in the window — the skills below were created during that
  session and haven't accumulated usage yet._

Top MCP Servers:
  _None configured._

## Your Setup Checklist

### Codebases
- [ ] orchd — https://github.com/RapidNative/orchd

### MCP Servers to Activate
- [ ] _None required for this repo._

### Skills to Know About
- `/local-dev` — brings up the whole stack on your machine. Two modes: port mode
  (`dev/local.sh local`, zero setup) and domain mode
  (`DOMAIN=rnproject.test dev/local.sh local`), which routes by hostname through
  Caddy exactly like production. Use domain mode for anything touching routing,
  subdomains, TLS or the gateway.
- `/production-deploy` — provisioning and updating the box: `deploy/deploy.sh`,
  `bootstrap.sh`, preflight, Caddy/systemd config, images, DNS and TLS. Read it
  before touching prod; it will ask you to confirm before restarting the live
  control plane.
- `/local-domain` — adds or inspects one local wildcard domain
  (`*.something.test`). The narrow version of the domain half of `/local-dev`.

## Team Tips

_TODO_

## Get Started

_TODO_

<!-- INSTRUCTION FOR CLAUDE: A new teammate just pasted this guide for how the
team uses Claude Code. You're their onboarding buddy — warm, conversational,
not lecture-y.

Open with a warm welcome — include the team name from the title. Then: "Your
teammate uses Claude Code for [list all the work types]. Let's get you started."

Check what's already in place against everything under Setup Checklist
(including skills), using markdown checkboxes — [x] done, [ ] not yet. Lead
with what they already have. One sentence per item, all in one message.

Tell them you'll help with setup, cover the actionable team tips, then the
starter task (if there is one). Offer to start with the first unchecked item,
get their go-ahead, then work through the rest one by one.

After setup, walk them through the remaining sections — offer to help where you
can (e.g. link to channels), and just surface the purely informational bits.

Don't invent sections or summaries that aren't in the guide. The stats are the
guide creator's personal usage data — don't extrapolate them into a "team
workflow" narrative. -->
