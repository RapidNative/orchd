import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { API_BASE, Code, Endpoint, type Group } from './parts'

const GROUPS: Group[] = [
  {
    id: 'health',
    title: 'Health',
    endpoints: [
      {
        method: 'GET',
        path: '/healthz',
        role: 'open',
        desc: 'Liveness probe. The only endpoint that never requires a key.',
        res: `{ "status": "ok", "auth": true }`,
      },
    ],
  },
  {
    id: 'projects',
    title: 'Projects',
    blurb:
      'A project is a logical grouping of workloads. Creating one provisions its workloads and returns their keys and endpoints.',
    endpoints: [
      {
        method: 'POST',
        path: '/v1/projects',
        role: 'admin',
        desc: 'Create a project + its workloads. Empty body creates one primary tinbase workload. `region` defaults to the default region.',
        req: `{
  "name": "my-app",           // optional
  "region": "local",          // optional (default region if omitted)
  "workloads": [              // optional (default: [{ "preset": "tinbase" }])
    { "preset": "tinbase" },
    { "preset": "vite" },
    { "preset": "api" }
  ]
}`,
        res: `{
  "id": "abc123",
  "name": "my-app",
  "region": "local",
  "created_at": "2026-07-19T...",
  "workloads": [
    {
      "id": "wid1", "type": "tinbase-project", "name": "",
      "state": "running", "memory_mb": 384, "cpus": 0.5,
      "keep_warm": false,
      "anon_key": "eyJ...", "service_role_key": "eyJ...",
      "routes": ["abc123.tinbase.dev"],
      "endpoints": ["https://abc123.tinbase.dev"],
      "subroutes": ["https://cloud.rapidnative.com/w/abc123"],
      "last_seen": "2026-07-19T..."
    }
  ]
}`,
      },
      {
        method: 'GET',
        path: '/v1/projects',
        role: 'readonly',
        desc: 'List all projects with their workloads.',
        res: `[ { "id": "...", "workloads": [...] } ]`,
      },
      {
        method: 'GET',
        path: '/v1/projects/{id}',
        role: 'readonly',
        desc: 'Get one project (workloads, keys, endpoints, routes).',
      },
      {
        method: 'DELETE',
        path: '/v1/projects/{id}',
        role: 'admin',
        desc: 'Stop and remove a project, all its workloads/routes, and its on-disk data. Destructive — 204 No Content.',
      },
    ],
  },
  {
    id: 'workloads',
    title: 'Workloads',
    blurb:
      'A workload is one routable, scale-to-zero instance. Presets: tinbase, expo, vite, api (see GET /v1/presets). Custom: type/image/port/memory_mb/cpus.',
    endpoints: [
      {
        method: 'POST',
        path: '/v1/projects/{id}/workloads',
        role: 'admin',
        desc: 'Add a workload to a project.',
        req: `{
  "preset": "vite",           // or set type/image/port explicitly
  "name": "web",              // optional role
  "image": "rn-vite:dev",     // optional (preset supplies it)
  "port": 8080,               // optional
  "memory_mb": 512,           // optional cap override
  "cpus": 1.0                 // optional cap override
}`,
        res: `{ "id": "wid...", "type": "rapidnative-dev", "name": "web", ... }`,
      },
      {
        method: 'GET',
        path: '/v1/workloads/{id}',
        role: 'readonly',
        desc: 'Get one workload (state, limits, keys, routes, last_seen).',
      },
      {
        method: 'DELETE',
        path: '/v1/workloads/{id}',
        role: 'admin',
        desc: 'Stop and remove one workload and its routes + data. 204.',
      },
      {
        method: 'POST',
        path: '/v1/workloads/{id}/keepwarm',
        role: 'admin',
        desc: 'Toggle always-on. Enabling boots the workload now and exempts it from scale-to-zero.',
        req: `{ "enabled": true }`,
        res: `{ "keep_warm": true }`,
      },
      {
        method: 'GET',
        path: '/v1/workloads/{id}/stats',
        role: 'readonly',
        desc: 'Live memory + CPU (docker stats). Empty snapshot when the workload is not running.',
        res: `{ "mem_usage": "76.2MiB / 384MiB", "mem_perc": "19.8%", "cpu_perc": "1.0%" }`,
      },
      {
        method: 'GET',
        path: '/v1/workloads/{id}/logs',
        role: 'readonly',
        desc: 'Container logs (stdout+stderr).',
        params: 'tail=<n> (default 200)',
        res: `{ "logs": "…last N lines…" }`,
      },
    ],
  },
  {
    id: 'domains',
    title: 'Domains (routes)',
    blurb:
      'Every workload gets a default subdomain. Attach custom domains (bring-your-own): point the domain (CNAME/A) at the gateway; a Let’s Encrypt cert is issued on first HTTPS request.',
    endpoints: [
      {
        method: 'POST',
        path: '/v1/workloads/{id}/routes',
        role: 'admin',
        desc: 'Attach a hostname (custom domain) to a workload.',
        req: `{ "host": "app.customer.com" }`,
        res: `{ ...workload with the new route in "routes"... }`,
      },
      {
        method: 'DELETE',
        path: '/v1/routes',
        role: 'admin',
        desc: 'Detach a hostname. 204.',
        params: 'host=<hostname> (query, required)',
      },
    ],
  },
  {
    id: 'images',
    title: 'Images',
    blurb:
      'Manage the Docker images a region’s daemon can launch as workloads. Requires the Docker driver (LocalDriver returns 501). Select a region with the optional region query/body param (empty = default region).',
    endpoints: [
      {
        method: 'GET',
        path: '/v1/images',
        role: 'readonly',
        desc: 'List images on a region’s Docker host (excludes dangling layers).',
        params: 'region=<id> (optional)',
        res: `[ { "repository": "rn-vite", "tag": "dev", "ref": "rn-vite:dev",
    "id": "a1b2c3d4e5f6", "size": "412MB", "created_at": "2 days ago" } ]`,
      },
      {
        method: 'POST',
        path: '/v1/images/pull',
        role: 'admin',
        desc: 'Pull an image tag onto a region’s Docker host. Blocks until the pull finishes; returns the CLI output.',
        req: `{ "ref": "ghcr.io/acme/app:1.2.0", "region": "" }`,
        res: `{ "ref": "ghcr.io/acme/app:1.2.0", "output": "…docker pull output…" }`,
      },
      {
        method: 'DELETE',
        path: '/v1/images',
        role: 'admin',
        desc: 'Remove an image by ref or id. Fails if a container still uses it unless force=true. 204.',
        params: 'ref=<ref|id> (required) · region=<id> (optional) · force=true (optional)',
      },
    ],
  },
  {
    id: 'backups',
    title: 'Backups',
    blurb:
      'Byte-exact volume snapshots (tar+gz). A backup is per-workload; a project backup fans out over its workloads. Target is local or S3/R2 (see Settings). Restoring replaces a workload’s current data.',
    endpoints: [
      {
        method: 'POST',
        path: '/v1/workloads/{id}/backups',
        role: 'admin',
        desc: 'Back up one workload now.',
        res: `{ "id": "wid__20260719T...", "workload_id": "wid", "created_at": "...", "size_bytes": 4797652 }`,
      },
      {
        method: 'POST',
        path: '/v1/projects/{id}/backups',
        role: 'admin',
        desc: 'Back up every workload in a project.',
        res: `[ { "id": "...", "workload_id": "...", "size_bytes": ... } ]`,
      },
      {
        method: 'GET',
        path: '/v1/backups',
        role: 'readonly',
        desc: 'List all backups (newest first).',
        res: `[ { "id": "...", "workload_id": "...", "created_at": "...", "size_bytes": ... } ]`,
      },
      {
        method: 'GET',
        path: '/v1/workloads/{id}/backups',
        role: 'readonly',
        desc: 'List backups for one workload.',
      },
      {
        method: 'POST',
        path: '/v1/workloads/{id}/restore',
        role: 'admin',
        desc: 'Restore a workload from a backup (replaces current data, then reboots it).',
        req: `{ "backup_id": "wid__20260719T..." }`,
        res: `{ "status": "restored" }`,
      },
      { method: 'DELETE', path: '/v1/backups/{id}', role: 'admin', desc: 'Delete a backup. 204.' },
    ],
  },
  {
    id: 'regions',
    title: 'Regions',
    blurb:
      'A region is a placement target; docker_host points it at a worker node’s Docker daemon (empty = local). Projects are placed in the default region unless one is chosen at create.',
    endpoints: [
      {
        method: 'GET',
        path: '/v1/regions',
        role: 'readonly',
        desc: 'List regions.',
        res: `[ { "id": "local", "name": "local", "docker_host": "", "is_default": true, "created_at": "..." } ]`,
      },
      {
        method: 'POST',
        path: '/v1/regions',
        role: 'admin',
        desc: 'Create a region (id is a slug of the name).',
        req: `{ "name": "EU West", "docker_host": "tcp://node2:2375" }`,
        res: `{ "id": "eu-west", "name": "EU West", "docker_host": "tcp://node2:2375", "is_default": false }`,
      },
      {
        method: 'DELETE',
        path: '/v1/regions/{id}',
        role: 'admin',
        desc: 'Delete a region (not the default — set another default first). 204.',
      },
      {
        method: 'POST',
        path: '/v1/regions/{id}/default',
        role: 'admin',
        desc: 'Make a region the default.',
        res: `{ "default": "eu-west" }`,
      },
    ],
  },
  {
    id: 'keys',
    title: 'API keys',
    blurb:
      'Multiple named keys with roles. The bootstrap key (server key file) is always admin. Keys are stored hashed; the plaintext is shown once at creation.',
    endpoints: [
      {
        method: 'GET',
        path: '/v1/keys',
        role: 'readonly',
        desc: 'List keys (no secrets).',
        res: `[ { "id": "...", "name": "ci-bot", "role": "readonly", "created_at": "..." } ]`,
      },
      {
        method: 'POST',
        path: '/v1/keys',
        role: 'admin',
        desc: 'Create a key. The plaintext key is returned exactly once.',
        req: `{ "name": "ci-bot", "role": "readonly" }`,
        res: `{ "key": "tbk_9f8e…", "meta": { "id": "...", "name": "ci-bot", "role": "readonly" } }`,
      },
      { method: 'DELETE', path: '/v1/keys/{id}', role: 'admin', desc: 'Revoke a key. 204.' },
    ],
  },
  {
    id: 'settings',
    title: 'Settings',
    blurb: 'Runtime-configurable platform settings. Secrets are stored server-side and never returned.',
    endpoints: [
      {
        method: 'GET',
        path: '/v1/settings',
        role: 'readonly',
        desc: 'Current backup target (secret masked), event webhook, and metrics sink.',
        res: `{
  "backup": { "type": "s3", "endpoint": "...", "bucket": "...", "region": "...", "access_key": "..." },
  "backup_secret_set": true,
  "webhook": { "url": "" },
  "metrics": { "type": "nop" }
}`,
      },
      {
        method: 'PUT',
        path: '/v1/settings/backup',
        role: 'admin',
        desc: 'Set the backup destination. Leave secret_key blank on an s3 target to keep the existing one.',
        req: `{
  "type": "s3",               // "local" | "s3"
  "endpoint": "https://<acct>.r2.cloudflarestorage.com",
  "bucket": "backups", "region": "auto", "prefix": "backups",
  "access_key": "…", "secret_key": "…"
}`,
      },
      {
        method: 'PUT',
        path: '/v1/settings/webhook',
        role: 'admin',
        desc: 'Set the event webhook URL (blank = off). Control-plane events are POSTed here as JSON.',
        req: `{ "url": "https://example.com/hooks" }`,
      },
      {
        method: 'PUT',
        path: '/v1/settings/metrics',
        role: 'admin',
        desc: 'Set the metrics sink.',
        req: `{ "type": "http", "url": "https://collector/metrics" }  // type: "nop" | "log" | "http"`,
      },
    ],
  },
  {
    id: 'observability',
    title: 'Metrics & events',
    endpoints: [
      {
        method: 'GET',
        path: '/v1/metrics',
        role: 'readonly',
        desc: 'Live fleet snapshot.',
        res: `{ "time": "...", "projects": 3, "workloads": 6, "running": 2, "suspended": 4, "mem_mb_allocated": 768 }`,
      },
      {
        method: 'GET',
        path: '/v1/events',
        role: 'readonly',
        desc: 'Recent control-plane events (audit feed), newest first.',
        params: 'limit=<n> (default 100)',
        res: `[ { "id": "...", "time": "...", "type": "project.created", "project_id": "abc123", "message": "" } ]`,
      },
    ],
  },
  {
    id: 'meta',
    title: 'Metadata',
    endpoints: [
      {
        method: 'GET',
        path: '/v1/presets',
        role: 'readonly',
        desc: 'Available workload presets.',
        res: `[ "api", "expo", "tinbase", "vite" ]`,
      },
      {
        method: 'GET',
        path: '/v1/info',
        role: 'readonly',
        desc: 'System configuration: driver, region, base domain, idle timeout, default resource limits, rate limit, backups/metrics status, image support, presets.',
        res: `{
  "driver": "docker+runsc", "region": "local", "base_domain": "tinbase.dev",
  "idle_timeout": "2m0s", "image": "tinbase:0.10.0", "rate_limit_per_min": 600,
  "limits": { "tinbase_mem_mb": 384, "tinbase_cpus": 0.5, "dev_mem_mb": 512, "dev_cpus": 1, "pids_limit": 512 },
  "backups": { "enabled": true, "interval": "24h0m0s", "retain": 7 },
  "images_supported": true,
  "metrics": { "type": "nop" },
  "presets": ["api","expo","tinbase","vite"]
}`,
      },
    ],
  },
  {
    id: 'internal',
    title: 'Internal',
    blurb: 'Used by the platform itself, not for general clients.',
    endpoints: [
      {
        method: 'GET',
        path: '/internal/tls-allow',
        role: 'open',
        desc: 'Caddy on-demand-TLS gate: returns 200 for admin/api and any host in the route table, 403 otherwise — so certificates are only issued for real hosts.',
        params: 'domain=<hostname>',
      },
    ],
  },
]

export function ApiReference() {
  return (
    <div>
      {/* Auth */}
      <Card id="auth" className="mb-6 scroll-mt-6">
        <CardHeader>
          <CardTitle className="text-base">Authentication</CardTitle>
          <p className="text-sm text-muted-foreground">
            Base URL: <code className="font-mono">{API_BASE}</code>
          </p>
        </CardHeader>
        <CardContent className="text-sm text-muted-foreground">
          <p>
            Every <code className="font-mono">/v1/*</code> endpoint requires an API key.{' '}
            <code className="font-mono">/healthz</code> is the only open endpoint.
          </p>
          <ul className="mt-3 list-disc space-y-1 pl-5">
            <li>
              Pass the key as <code className="font-mono">Authorization: Bearer &lt;key&gt;</code> or{' '}
              <code className="font-mono">X-API-Key: &lt;key&gt;</code>.
            </li>
            <li>
              The <b className="text-foreground">bootstrap key</b> (from the server key file) is
              always <b className="text-foreground">admin</b>. You can mint more keys (
              <code className="font-mono">POST /v1/keys</code>) with a role.
            </li>
            <li>
              <b className="text-foreground">Roles:</b> <span className="text-primary">any key</span>{' '}
              (readonly) may call every <code className="font-mono">GET</code>;{' '}
              <span className="text-warning">admin</span> is required for any{' '}
              <code className="font-mono">POST/PUT/DELETE</code>. A readonly key on a mutating call →
              403.
            </li>
            <li>Missing/invalid key → 401. Over the per-key rate limit → 429 (Retry-After).</li>
          </ul>
          <Code title="Example">{`curl ${API_BASE}/v1/projects \\
  -H "Authorization: Bearer $TINBASE_KEY"

curl -X POST ${API_BASE}/v1/projects \\
  -H "Authorization: Bearer $TINBASE_KEY" \\
  -H "Content-Type: application/json" \\
  -d '{"workloads":[{"preset":"tinbase"}]}'`}</Code>
          <p className="mt-3">
            Errors are JSON: <code className="font-mono">{`{ "error": "…" }`}</code>. Status codes:
            200/201 ok, 204 no content, 400 bad request, 401 unauthorized, 403 forbidden (role), 404
            not found, 409 conflict, 429 rate limited, 501 not implemented (capability off).
          </p>
        </CardContent>
      </Card>

      {/* Endpoint groups */}
      {GROUPS.map((g) => (
        <Card key={g.id} id={g.id} className="mb-6 scroll-mt-6">
          <CardHeader>
            <CardTitle className="text-base">{g.title}</CardTitle>
            {g.blurb && <p className="text-sm text-muted-foreground">{g.blurb}</p>}
          </CardHeader>
          <CardContent className="pt-0">
            {g.endpoints.map((e) => (
              <Endpoint key={e.method + e.path} e={e} />
            ))}
          </CardContent>
        </Card>
      ))}
    </div>
  )
}
