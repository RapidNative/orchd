import type { ReactNode } from 'react'
import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { PageHeader } from '@/components/bits'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'

function Row({ k, v }: { k: string; v: ReactNode }) {
  return (
    <div className="flex items-center justify-between border-b border-border/60 py-2 last:border-0">
      <span className="text-sm text-muted-foreground">{k}</span>
      <span className="font-mono text-sm text-foreground">{v}</span>
    </div>
  )
}

export function System() {
  const info = useQuery({ queryKey: ['info'], queryFn: api.info })
  const d = info.data

  return (
    <div>
      <PageHeader title="System" subtitle="Orchestrator configuration (from /v1/info)" />
      <div className="grid gap-4 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Platform</CardTitle>
          </CardHeader>
          <CardContent>
            <Row k="Driver" v={d?.driver ?? '—'} />
            <Row k="Region" v={d?.region ?? '—'} />
            <Row k="Base domain" v={d?.base_domain ?? '—'} />
            <Row k="Idle timeout" v={d?.idle_timeout ?? '—'} />
            <Row k="tinbase image" v={d?.image ?? '—'} />
            <Row k="Presets" v={d?.presets?.join(', ') ?? '—'} />
          </CardContent>
        </Card>
        <Card>
          <CardHeader>
            <CardTitle className="text-base">Default resource limits</CardTitle>
          </CardHeader>
          <CardContent>
            <Row
              k="tinbase workload"
              v={d ? `${d.limits.tinbase_mem_mb}MB · ${d.limits.tinbase_cpus} CPU` : '—'}
            />
            <Row
              k="dev workload"
              v={d ? `${d.limits.dev_mem_mb}MB · ${d.limits.dev_cpus} CPU` : '—'}
            />
            <Row k="pids limit" v={d?.limits.pids_limit ?? '—'} />
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
