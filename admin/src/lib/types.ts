export interface Workload {
  id: string
  project_id: string
  type: string
  name: string
  image?: string
  port?: number
  memory_mb?: number
  cpus?: number
  keep_warm?: boolean
  state: 'provisioning' | 'running' | 'suspended' | 'stopped' | 'failed'
  anon_key?: string
  service_role_key?: string
  created_at: string
  routes: string[]
  endpoints: string[]
  subroutes: string[]
  last_seen?: string
}

export interface Project {
  id: string
  name?: string
  region: string
  created_at: string
  workloads: Workload[]
}

export interface Stats {
  mem_usage: string
  mem_perc: string
  cpu_perc: string
}

export interface Backup {
  id: string
  workload_id: string
  created_at: string
  size_bytes: number
}

export interface BackupTarget {
  type: 'local' | 's3'
  endpoint?: string
  bucket?: string
  region?: string
  prefix?: string
  access_key?: string
  secret_key?: string
}

export interface MetricsTarget {
  type: 'nop' | 'log' | 'http'
  url?: string
}

export interface SettingsResp {
  instance_name: string
  backup: BackupTarget
  backup_secret_set: boolean
  webhook: { url: string }
  metrics: MetricsTarget
}

export interface Event {
  id: string
  time: string
  type: string
  project_id?: string
  workload_id?: string
  message?: string
}

export interface Region {
  id: string
  name: string
  docker_host?: string
  is_default: boolean
  created_at: string
}

export interface ApiKeyMeta {
  id: string
  name: string
  role: 'admin' | 'readonly'
  created_at: string
}

export interface Info {
  instance_name: string
  region: string
  driver: string
  base_domain: string
  public_url: string
  idle_timeout: string
  image: string
  limits: {
    tinbase_mem_mb: number
    tinbase_cpus: number
    dev_mem_mb: number
    dev_cpus: number
    pids_limit: number
  }
  backups: {
    enabled: boolean
    interval: string
    retain: number
  }
  images_supported: boolean
  presets: string[]
}

export interface Image {
  repository: string
  tag: string
  ref: string
  id: string
  digest: string
  size: string
  created_at: string
}

export interface WorkloadSpecReq {
  preset?: string
  type?: string
  name?: string
  image?: string
  port?: number
  memory_mb?: number
  cpus?: number
}
