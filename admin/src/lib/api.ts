import { auth } from './auth'
import type { Backup, Info, Project, Stats, WorkloadSpecReq } from './types'

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
  createProject: (body: { name?: string; workloads?: WorkloadSpecReq[] }) =>
    req<Project>('/v1/projects', { method: 'POST', body: JSON.stringify(body) }),
  deleteProject: (id: string) => req<null>('/v1/projects/' + id, { method: 'DELETE' }),
  addWorkload: (id: string, body: WorkloadSpecReq) =>
    req('/v1/projects/' + id + '/workloads', { method: 'POST', body: JSON.stringify(body) }),
  deleteWorkload: (id: string) => req<null>('/v1/workloads/' + id, { method: 'DELETE' }),
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
}
