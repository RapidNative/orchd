import { auth, OPEN_KEY } from './auth'
import type {
  ApiKeyMeta,
  Backup,
  BackupTarget,
  BuiltImage,
  Event,
  Image,
  ImageImportSpec,
  Info,
  MetricsTarget,
  Project,
  Region,
  SettingsResp,
  Stats,
  WorkloadSpecReq,
} from './types'

const BASE = import.meta.env.VITE_API_BASE ?? '/api'

// authHeaders is empty when the control plane runs without a key (local dev).
function authHeaders(): Record<string, string> {
  const k = auth.get()
  return k && k !== OPEN_KEY ? { Authorization: 'Bearer ' + k } : {}
}

export class ApiError extends Error {
  status: number
  constructor(message: string, status: number) {
    super(message)
    this.status = status
  }
}

async function req<T>(path: string, opts: RequestInit = {}): Promise<T> {
  const res = await fetch(BASE + path, {
    ...opts,
    headers: {
      ...authHeaders(),
      'Content-Type': 'application/json',
      ...(opts.headers ?? {}),
    },
  })
  if (res.status === 401) throw new ApiError('unauthorized', 401)
  if (!res.ok) {
    let msg = res.statusText
    try {
      msg = (await res.json()).error ?? msg
    } catch {
      /* keep statusText */
    }
    throw new ApiError(msg, res.status)
  }
  if (res.status === 204) return null as T
  return res.json() as Promise<T>
}

// reqText is like req but returns the raw response body as text (file content,
// which is served as text/plain rather than JSON).
async function reqText(path: string): Promise<string> {
  const res = await fetch(BASE + path, { headers: authHeaders() })
  if (res.status === 401) throw new ApiError('unauthorized', 401)
  if (!res.ok) throw new ApiError(res.statusText, res.status)
  return res.text()
}

const withWorkloads = <T extends { workloads?: Project['workloads'] }>(p: T): T =>
  p.workloads ? p : { ...p, workloads: [] }

export const api = {
  info: () => req<Info>('/v1/info'),
  // `workloads` is declared as an array but older servers send null for a
  // project that has none (mid-create, or all workloads deleted). Normalising
  // here keeps every consumer free of null checks — the components map over it
  // in a dozen places and a null took the whole page down.
  projects: async () => (await req<Project[]>('/v1/projects')).map(withWorkloads),
  project: async (id: string) => withWorkloads(await req<Project>('/v1/projects/' + id)),
  createProject: (body: {
    name?: string
    region?: string
    template?: string
    image?: string
    delta?: Record<string, string>
    deleted?: string[]
    workloads?: WorkloadSpecReq[]
  }) => req<Project>('/v1/projects', { method: 'POST', body: JSON.stringify(body) }),
  templates: () => req<Record<string, string>>('/v1/templates'),
  setTemplate: (name: string, path: string) =>
    req<Record<string, string>>('/v1/templates', {
      method: 'PUT',
      body: JSON.stringify({ name, path }),
    }),
  template: (name: string) =>
    req<{ name: string; workloads: { name: string; kind: string; image?: string; dir?: string }[] }>(
      '/v1/templates/' + encodeURIComponent(name),
    ),
  templateFiles: (name: string) => req<string[]>('/v1/templates/' + encodeURIComponent(name) + '/files'),
  templateFile: (name: string, path: string) =>
    reqText('/v1/templates/' + encodeURIComponent(name) + '/files?path=' + encodeURIComponent(path)),
  templateBundleUrl: (name: string) => BASE + '/v1/templates/' + encodeURIComponent(name) + '/bundle',
  imageBundleUrl: (template: string, version: string) =>
    BASE + '/v1/built-images/' + encodeURIComponent(template) + '/' + encodeURIComponent(version) + '/bundle',
  buildImage: (name: string) =>
    req<BuiltImage>('/v1/templates/' + encodeURIComponent(name) + '/build', { method: 'POST' }),
  builtImages: () => req<BuiltImage[]>('/v1/built-images'),
  deleteBuiltImage: (template: string, version: string) =>
    req<null>(
      '/v1/built-images/' + encodeURIComponent(template) + '/' + encodeURIComponent(version),
      { method: 'DELETE' },
    ),
  pushImage: (template: string, version: string) =>
    req<{ registry: Record<string, string> }>(
      '/v1/built-images/' + encodeURIComponent(template) + '/' + encodeURIComponent(version) + '/push',
      { method: 'POST' },
    ),
  imageSpec: (template: string, version: string) =>
    req<ImageImportSpec>(
      '/v1/built-images/' + encodeURIComponent(template) + '/' + encodeURIComponent(version) + '/spec',
    ),
  importImage: (spec: ImageImportSpec) =>
    req<BuiltImage>('/v1/built-images/import', { method: 'POST', body: JSON.stringify(spec) }),
  workloadFiles: (id: string) => req<string[]>('/v1/workloads/' + id + '/fs'),
  workloadFile: (id: string, path: string) =>
    reqText('/v1/workloads/' + id + '/fs/file?path=' + encodeURIComponent(path)),
  writeWorkloadFile: (id: string, path: string, content: string) =>
    req<{ path: string; bytes: number }>(
      '/v1/workloads/' + id + '/fs/file?path=' + encodeURIComponent(path),
      { method: 'PUT', headers: { 'Content-Type': 'text/plain' }, body: content },
    ),
  deleteWorkloadFile: (id: string, path: string) =>
    req<null>('/v1/workloads/' + id + '/fs/file?path=' + encodeURIComponent(path), {
      method: 'DELETE',
    }),
  regions: () => req<Region[]>('/v1/regions'),
  createRegion: (name: string, docker_host?: string) =>
    req<Region>('/v1/regions', { method: 'POST', body: JSON.stringify({ name, docker_host }) }),
  deleteRegion: (id: string) => req<null>('/v1/regions/' + id, { method: 'DELETE' }),
  setDefaultRegion: (id: string) => req('/v1/regions/' + id + '/default', { method: 'POST' }),
  keys: () => req<ApiKeyMeta[]>('/v1/keys'),
  createKey: (name: string, role: string) =>
    req<{ key: string; meta: ApiKeyMeta }>('/v1/keys', {
      method: 'POST',
      body: JSON.stringify({ name, role }),
    }),
  deleteKey: (id: string) => req<null>('/v1/keys/' + id, { method: 'DELETE' }),
  addRoute: (workloadId: string, host: string) =>
    req('/v1/workloads/' + workloadId + '/routes', { method: 'POST', body: JSON.stringify({ host }) }),
  removeRoute: (host: string) =>
    req<null>('/v1/routes?host=' + encodeURIComponent(host), { method: 'DELETE' }),
  deleteProject: (id: string) => req<null>('/v1/projects/' + id, { method: 'DELETE' }),
  addWorkload: (id: string, body: WorkloadSpecReq) =>
    req('/v1/projects/' + id + '/workloads', { method: 'POST', body: JSON.stringify(body) }),
  deleteWorkload: (id: string) => req<null>('/v1/workloads/' + id, { method: 'DELETE' }),
  setKeepWarm: (id: string, enabled: boolean) =>
    req('/v1/workloads/' + id + '/keepwarm', { method: 'POST', body: JSON.stringify({ enabled }) }),
  setWorkloadEnv: (id: string, env: Record<string, string>) =>
    req<{ env: Record<string, string> }>('/v1/workloads/' + id + '/env', {
      method: 'PUT',
      body: JSON.stringify({ env }),
    }),
  startWorkload: (id: string) => req('/v1/workloads/' + id + '/start', { method: 'POST' }),
  stopWorkload: (id: string) => req('/v1/workloads/' + id + '/stop', { method: 'POST' }),
  restartWorkload: (id: string) => req('/v1/workloads/' + id + '/restart', { method: 'POST' }),
  startProject: (id: string) => req('/v1/projects/' + id + '/start', { method: 'POST' }),
  stopProject: (id: string) => req('/v1/projects/' + id + '/stop', { method: 'POST' }),
  restartProject: (id: string) => req('/v1/projects/' + id + '/restart', { method: 'POST' }),
  setProjectEnv: (id: string, env: Record<string, string>) =>
    req<{ env: Record<string, string> }>('/v1/projects/' + id + '/env', {
      method: 'PUT',
      body: JSON.stringify({ env }),
    }),
  stats: (id: string) => req<Stats>('/v1/workloads/' + id + '/stats'),
  logs: (id: string, tail = 200) =>
    req<{ logs: string }>('/v1/workloads/' + id + '/logs?tail=' + tail),
  backups: () => req<Backup[]>('/v1/backups'),
  createBackup: (workloadId: string) =>
    req<Backup>('/v1/workloads/' + workloadId + '/backups', { method: 'POST' }),
  restore: (workloadId: string, backupId: string) =>
    req('/v1/workloads/' + workloadId + '/restore', {
      method: 'POST',
      body: JSON.stringify({ backup_id: backupId }),
    }),
  deleteBackup: (id: string) => req<null>('/v1/backups/' + id, { method: 'DELETE' }),
  backupProject: (projectId: string) =>
    req<Backup[]>('/v1/projects/' + projectId + '/backups', { method: 'POST' }),
  settings: () => req<SettingsResp>('/v1/settings'),
  setBackupTarget: (t: BackupTarget) =>
    req<{ backup: BackupTarget }>('/v1/settings/backup', {
      method: 'PUT',
      body: JSON.stringify(t),
    }),
  setInstanceName: (instance_name: string) =>
    req<{ instance_name: string }>('/v1/settings/name', {
      method: 'PUT',
      body: JSON.stringify({ instance_name }),
    }),
  setRegistry: (registry: string) =>
    req<{ registry: string }>('/v1/settings/registry', {
      method: 'PUT',
      body: JSON.stringify({ registry }),
    }),
  backupState: () => req<Backup>('/v1/system/backup', { method: 'POST' }),
  setWebhook: (url: string) =>
    req<{ url: string }>('/v1/settings/webhook', { method: 'PUT', body: JSON.stringify({ url }) }),
  setMetrics: (t: MetricsTarget) =>
    req<MetricsTarget>('/v1/settings/metrics', { method: 'PUT', body: JSON.stringify(t) }),
  events: (limit = 100) => req<Event[]>('/v1/events?limit=' + limit),
  images: (region?: string) =>
    req<Image[]>('/v1/images' + (region ? '?region=' + encodeURIComponent(region) : '')),
  pullImage: (ref: string, region?: string) =>
    req<{ ref: string; output: string }>('/v1/images/pull', {
      method: 'POST',
      body: JSON.stringify({ ref, region: region ?? '' }),
    }),
  removeImage: (ref: string, region?: string, force = false) =>
    req<null>(
      '/v1/images?ref=' +
        encodeURIComponent(ref) +
        (region ? '&region=' + encodeURIComponent(region) : '') +
        (force ? '&force=true' : ''),
      { method: 'DELETE' },
    ),
}
