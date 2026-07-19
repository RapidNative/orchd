export interface Workload {
  id: string
  project_id: string
  type: string
  name: string
  image?: string
  port?: number
  memory_mb?: number
  cpus?: number
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

export interface Info {
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
  presets: string[]
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
