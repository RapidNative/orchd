# Adopter configs

ORCHD's engine has no product built in. A product running on it supplies an
**adopter config** (`ORCHD_ADOPTER_CONFIG`, schema in the top-level README and
`orchestrator/internal/adopter`): its presets, the label of its app workloads, how
the local process driver runs its images, its readiness-probe headers and its env
conventions.

| Adopter | File | Used by |
| --- | --- | --- |
| RapidNative | [`rapidnative/adopter.json`](rapidnative/adopter.json) | `deploy/orchd.service`, `deploy/orchd.service.production` (copied to `/opt/orchd/adopter.json` by `deploy/deploy.sh`), `dev/local.sh` |

## What else in this repo belongs to one adopter

These still live here because RapidNative's deployment builds and ships from
this repo. None of them is read by the engine.

| Path | Whose | Recommendation |
| --- | --- | --- |
| `deploy/adopters/rapidnative/` | RapidNative | keep here as the reference example, or move to a RapidNative-owned deploy repo |
| `deploy/orchd.service*`, `deploy/Caddyfile*`, `deploy/deploy.sh`, `deploy/bootstrap.sh`, `deploy/build-template.sh`, `deploy/publish-docs.sh` | RapidNative's production and staging boxes (domains, relay hosts, image tags) | move to a RapidNative-owned deploy repo; leave a generic example unit and Caddyfile here |
| `orchestrator/images/rn-*` | RapidNative's runner images | move next to RapidNative's templates; deploy.sh copies them to the box today |
| `template-examples/rapidnative/` | RapidNative's project template | fine as an example template; mark it as one adopter's |
| `.claude/skills/production-deploy`, `local-dev`, `provisioning` | RapidNative's operators | move with the deploy files |

Mentions of RapidNative in the README and docs as a user of ORCHD are fine.
