import { auth } from './auth'
import type {
  ApiKeyMeta,
  Backup,
  BackupTarget,
  Event,
  Image,
  Info,
  MetricsTarget,
  Project,
  Region,
  SettingsResp,
  Stats,
  WorkloadSpecReq,
} from './types'

const BASE = import.meta.env.VITE_API_BASE ?? '/api'

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
      Authorization: 'Bearer ' + auth.get(),
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

export const api = {
  info: () => req<Info>('/v1/info'),
  projects: () => req<Project[]>('/v1/projects'),
  project: (id: string) => req<Project>('/v1/projects/' + id),
  createProject: (body: {
    name?: string
    region?: string
    template?: string
    workloads?: WorkloadSpecReq[]
  }) => req<Project>('/v1/projects', { method: 'POST', body: JSON.stringify(body) }),
  templates: () => req<Record<string, string>>('/v1/templates'),
  setTemplate: (name: string, path: string) =>
    req<Record<string, string>>('/v1/templates', {
      method: 'PUT',
      body: JSON.stringify({ name, path }),
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
