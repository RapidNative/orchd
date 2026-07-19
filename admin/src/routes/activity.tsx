import { useQuery } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { PageHeader } from '@/components/bits'
import { Card } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { relativeTime } from '@/lib/utils'

const tone: Record<string, string> = {
  created: 'running',
  restored: 'running',
  deleted: 'failed',
}
function badgeFor(type: string) {
  const verb = type.split('.')[1] ?? ''
  return tone[verb] ?? 'neutral'
}

export function Activity() {
  const events = useQuery({ queryKey: ['events'], queryFn: () => api.events(200), refetchInterval: 5000 })

  return (
    <div>
      <PageHeader title="Activity" subtitle="Control-plane events (audit feed)" />
      <Card className="p-2">
        {!events.data?.length ? (
          <p className="p-4 text-sm text-muted-foreground">No activity yet.</p>
        ) : (
          <div className="flex flex-col divide-y divide-border/60">
            {events.data.map((e) => (
              <div key={e.id} className="flex items-center justify-between gap-4 px-3 py-2.5">
                <div className="flex min-w-0 items-center gap-3">
                  <Badge variant={badgeFor(e.type) as never}>{e.type}</Badge>
                  <span className="truncate font-mono text-xs text-muted-foreground">
                    {e.project_id && <span className="text-foreground">{e.project_id}</span>}
                    {e.workload_id ? ` / ${e.workload_id}` : ''}
                    {e.message ? `  ${e.message}` : ''}
                  </span>
                </div>
                <span className="shrink-0 text-xs text-muted-foreground">{relativeTime(e.time)}</span>
              </div>
            ))}
          </div>
        )}
      </Card>
    </div>
  )
}
