import { useQuery } from '@tanstack/react-query'
import { Link } from '@tanstack/react-router'
import { api } from '@/lib/api'
import { PageHeader, StatCard } from '@/components/bits'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { relativeTime } from '@/lib/utils'

export function Dashboard() {
  const info = useQuery({ queryKey: ['info'], queryFn: api.info })
  // Counts come from the lightweight metrics snapshot; the list fetches only the
  // 6 most-recently-active projects — the dashboard no longer pulls the whole fleet.
  const metrics = useQuery({ queryKey: ['metrics'], queryFn: api.metrics, refetchInterval: 5000 })
  const recent = useQuery({
    queryKey: ['projects', 'recent'],
    queryFn: () => api.projectsPage({ page: 0, pageSize: 6, sort: 'recent' }),
    refetchInterval: 5000,
  })
  const recentProjects = recent.data?.items ?? []

  return (
    <div>
      <PageHeader
        title="Dashboard"
        subtitle={info.data ? `${info.data.driver} · region ${info.data.region}` : 'loading…'}
      />

      <div className="grid grid-cols-2 gap-3 md:grid-cols-4">
        <StatCard label="Projects" value={metrics.data?.projects ?? '—'} />
        <StatCard
          label="Workloads"
          value={metrics.data?.workloads ?? '—'}
          sub={`${metrics.data?.running ?? 0} running`}
        />
        <StatCard
          label="Idle timeout"
          value={info.data?.idle_timeout ?? '—'}
          sub="then scale-to-zero"
        />
        <StatCard
          label="tinbase cap"
          value={info.data ? `${info.data.limits.tinbase_mem_mb}MB` : '—'}
          sub={info.data ? `${info.data.limits.tinbase_cpus} CPU` : undefined}
        />
      </div>

      <Card className="mt-6">
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-base">Recent projects</CardTitle>
          <Link to="/projects" className="text-sm text-accent hover:underline">
            View all →
          </Link>
        </CardHeader>
        <CardContent>
          {!recentProjects.length ? (
            <p className="text-sm text-muted-foreground">No projects yet.</p>
          ) : (
            <div className="flex flex-col divide-y divide-border/60">
              {recentProjects.map((p) => {
                const run = p.workloads.filter((w) => w.state === 'running').length
                return (
                  <Link
                    key={p.id}
                    to="/projects/$id"
                    params={{ id: p.id }}
                    className="flex items-center justify-between py-2.5 hover:opacity-80"
                  >
                    <div className="flex items-center gap-3">
                      <span className="font-mono text-sm">{p.id}</span>
                      {p.name && <span className="text-xs text-muted-foreground">{p.name}</span>}
                    </div>
                    <div className="flex items-center gap-3">
                      <span className="text-xs text-muted-foreground">
                        {p.workloads.length} workload{p.workloads.length === 1 ? '' : 's'}
                      </span>
                      <Badge variant={run ? 'running' : 'suspended'}>
                        {run ? `${run} running` : 'idle'}
                      </Badge>
                      <span className="w-16 text-right text-xs text-muted-foreground">
                        {relativeTime(p.last_active_at ?? p.created_at)}
                      </span>
                    </div>
                  </Link>
                )
              })}
            </div>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
