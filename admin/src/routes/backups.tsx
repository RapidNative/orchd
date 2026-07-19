import { useMemo } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { PageHeader } from '@/components/bits'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { IconRefresh, IconTrash } from '@/components/icons'
import { Pager, SearchBox, usePaged } from '@/components/paged'
import { formatBytes, relativeTime } from '@/lib/utils'

export function Backups() {
  const qc = useQueryClient()
  const info = useQuery({ queryKey: ['info'], queryFn: api.info })
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects })
  const backups = useQuery({
    queryKey: ['backups'],
    queryFn: api.backups,
    refetchInterval: 8000,
    enabled: info.data?.backups.enabled !== false,
  })

  // Map workload id -> {project, name} for readable labels.
  const wmap = useMemo(() => {
    const m = new Map<string, { project: string; name: string }>()
    for (const p of projects.data ?? [])
      for (const w of p.workloads) m.set(w.id, { project: p.id, name: w.name || 'primary' })
    return m
  }, [projects.data])

  const restore = useMutation({
    mutationFn: ({ wid, bid }: { wid: string; bid: string }) => api.restore(wid, bid),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['projects'] }),
  })
  const del = useMutation({
    mutationFn: (id: string) => api.deleteBackup(id),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['backups'] }),
  })

  // Enrich each backup with its project + workload, then group by project
  // (project asc, newest first within a project) so restores line up with the
  // project they belong to — a backup is per-workload, but you think in projects.
  const rows = useMemo(() => {
    const list = (backups.data ?? []).map((b) => {
      const w = wmap.get(b.workload_id)
      return { ...b, project: w?.project ?? '', wname: w?.name ?? '' }
    })
    list.sort(
      (a, b) =>
        a.project.localeCompare(b.project) ||
        new Date(b.created_at).getTime() - new Date(a.created_at).getTime(),
    )
    return list
  }, [backups.data, wmap])

  const paged = usePaged(
    rows,
    (b, q) => b.project.toLowerCase().includes(q) || b.wname.toLowerCase().includes(q),
    12,
  )

  const enabled = info.data?.backups.enabled

  return (
    <div>
      <PageHeader
        title="Backups"
        subtitle={
          enabled
            ? `local store · every ${info.data?.backups.interval} · keep ${info.data?.backups.retain}`
            : 'durability for project data'
        }
        actions={
          <>
            {enabled && (
              <SearchBox
                value={paged.q}
                onChange={paged.setQ}
                placeholder="Search project / workload…"
              />
            )}
            <Button variant="secondary" onClick={() => backups.refetch()} title="Refresh">
              <IconRefresh />
            </Button>
          </>
        }
      />

      {enabled === false && (
        <Card>
          <CardHeader className="flex-row items-center justify-between">
            <CardTitle className="text-base">Not configured</CardTitle>
            <Badge variant="warning">disabled</Badge>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground">
              Set <code className="font-mono">ORCHD_BACKUP_DIR</code> on the orchestrator to enable
              backups.
            </p>
          </CardContent>
        </Card>
      )}

      {enabled && (
        <>
          <div className="mb-4 rounded-lg border border-warning/30 bg-warning/5 px-4 py-2.5 text-sm text-muted-foreground">
            Backups are stored <b className="text-foreground">on the box</b> — they protect against
            accidental deletes and corruption, but not box loss. Off-box (S3/R2) is the next step.
            Restoring replaces the workload's current data.
          </div>

          <Card>
            <Table>
              <THead>
                <TR>
                  <TH>Project</TH>
                  <TH>Workload</TH>
                  <TH>Created</TH>
                  <TH>Size</TH>
                  <TH className="text-right">Actions</TH>
                </TR>
              </THead>
              <TBody>
                {paged.pageItems.map((b, i) => {
                  const firstOfGroup = i === 0 || paged.pageItems[i - 1].project !== b.project
                  return (
                    <TR key={b.id}>
                      <TD className="font-mono">
                        {firstOfGroup ? b.project || '—' : <span className="opacity-0">{b.project}</span>}
                      </TD>
                      <TD className="font-mono text-muted-foreground">
                        {b.wname || <span className="italic">deleted</span>}
                      </TD>
                      <TD className="text-xs text-muted-foreground">
                        {new Date(b.created_at).toLocaleString()} ({relativeTime(b.created_at)})
                      </TD>
                      <TD className="font-mono text-xs">{formatBytes(b.size_bytes)}</TD>
                      <TD className="text-right">
                        <div className="flex items-center justify-end gap-1.5">
                          <Button
                            size="sm"
                            variant="secondary"
                            disabled={!b.wname || restore.isPending}
                            onClick={() => {
                              if (
                                confirm(
                                  `Restore ${b.project}/${b.wname} from ${new Date(
                                    b.created_at,
                                  ).toLocaleString()}? This REPLACES the current data.`,
                                )
                              )
                                restore.mutate({ wid: b.workload_id, bid: b.id })
                            }}
                          >
                            Restore
                          </Button>
                          <Button
                            size="sm"
                            variant="destructive"
                            onClick={() => {
                              if (confirm('Delete this backup?')) del.mutate(b.id)
                            }}
                          >
                            <IconTrash />
                          </Button>
                        </div>
                      </TD>
                    </TR>
                  )
                })}
              </TBody>
            </Table>
            {backups.data && backups.data.length === 0 && (
              <p className="p-6 text-sm text-muted-foreground">
                No backups yet. Trigger one from a project's page (Backup now), or wait for the
                scheduled run.
              </p>
            )}
          </Card>
          <Pager
            page={paged.page}
            pageCount={paged.pageCount}
            total={paged.total}
            onPage={paged.setPage}
          />
        </>
      )}
    </div>
  )
}
