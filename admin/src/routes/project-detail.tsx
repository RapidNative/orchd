import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { Link, useNavigate, useParams } from '@tanstack/react-router'
import { api } from '@/lib/api'
import type { Workload } from '@/lib/types'
import { CopyButton, PageHeader } from '@/components/bits'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { Tabs, TabsContent, TabsList, TabsTrigger } from '@/components/ui/tabs'
import { IconBackups, IconExternal, IconPlus, IconTrash } from '@/components/icons'
import { relativeTime } from '@/lib/utils'

function primaryUrl(w: Workload) {
  return w.endpoints?.[0] || w.subroutes?.[0] || ''
}
function openUrl(w: Workload) {
  const u = primaryUrl(w)
  return w.type === 'tinbase-project' && u ? u + '/_/' : u
}

function WorkloadStats({ w }: { w: Workload }) {
  const q = useQuery({
    queryKey: ['stats', w.id],
    queryFn: () => api.stats(w.id),
    refetchInterval: 3000,
    enabled: w.state === 'running',
  })
  if (w.state !== 'running') return <span className="text-muted-foreground">—</span>
  if (!q.data?.mem_usage) return <span className="text-muted-foreground">…</span>
  return (
    <span className="font-mono text-xs">
      {q.data.mem_usage} <span className="text-muted-foreground">({q.data.mem_perc})</span>
    </span>
  )
}

function LogsView({ id }: { id: string }) {
  const q = useQuery({
    queryKey: ['logs', id],
    queryFn: () => api.logs(id, 300),
    refetchInterval: 5000,
  })
  return (
    <pre className="mt-3 max-h-96 overflow-auto rounded-lg border border-border bg-[#080b12] p-3 font-mono text-xs leading-relaxed text-muted-foreground">
      {q.data?.logs?.trim() || (q.isLoading ? 'loading…' : 'no logs')}
    </pre>
  )
}

export function ProjectDetail() {
  const params = useParams({ strict: false }) as { id: string }
  const id = params.id
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [preset, setPreset] = useState('tinbase')

  const project = useQuery({
    queryKey: ['project', id],
    queryFn: () => api.project(id),
    refetchInterval: 5000,
  })
  const info = useQuery({ queryKey: ['info'], queryFn: api.info })

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ['project', id] })
    qc.invalidateQueries({ queryKey: ['projects'] })
  }
  const delProject = useMutation({
    mutationFn: () => api.deleteProject(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['projects'] })
      navigate({ to: '/projects' })
    },
  })
  const addWorkload = useMutation({
    mutationFn: () => api.addWorkload(id, { preset }),
    onSuccess: invalidate,
  })
  const delWorkload = useMutation({
    mutationFn: (wid: string) => api.deleteWorkload(wid),
    onSuccess: invalidate,
  })
  const backup = useMutation({
    mutationFn: (wid: string) => api.createBackup(wid),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ['backups'] })
      invalidate()
    },
  })

  const p = project.data
  const workloads = p?.workloads ?? []

  return (
    <div>
      <div className="mb-2">
        <Link to="/projects" className="text-sm text-muted-foreground hover:text-foreground">
          ← Projects
        </Link>
      </div>
      <PageHeader
        title={id}
        subtitle={p ? `${p.name || 'project'} · region ${p.region} · ${relativeTime(p.created_at)}` : ''}
        actions={
          <Button
            variant="destructive"
            disabled={delProject.isPending}
            onClick={() => {
              if (confirm(`Delete project ${id} and all its data? This cannot be undone.`))
                delProject.mutate()
            }}
          >
            <IconTrash /> Delete project
          </Button>
        }
      />

      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-base">Workloads</CardTitle>
          <div className="flex items-center gap-2">
            <select
              value={preset}
              onChange={(e) => setPreset(e.target.value)}
              className="h-8 rounded-md border border-border bg-input px-2 font-mono text-xs text-foreground"
            >
              {(info.data?.presets ?? ['tinbase', 'expo', 'vite', 'api']).map((x) => (
                <option key={x} value={x}>
                  {x}
                </option>
              ))}
            </select>
            <Button size="sm" disabled={addWorkload.isPending} onClick={() => addWorkload.mutate()}>
              <IconPlus /> Add
            </Button>
          </div>
        </CardHeader>
        <CardContent>
          <Table>
            <THead>
              <TR>
                <TH>Name</TH>
                <TH>Type</TH>
                <TH>State</TH>
                <TH>Memory</TH>
                <TH>Limits</TH>
                <TH>Last hit</TH>
                <TH>Route</TH>
                <TH></TH>
              </TR>
            </THead>
            <TBody>
              {workloads.map((w) => (
                <TR key={w.id}>
                  <TD className="font-mono">{w.name || '(primary)'}</TD>
                  <TD className="text-xs text-muted-foreground">{w.type}</TD>
                  <TD>
                    <Badge variant={w.state}>{w.state}</Badge>
                  </TD>
                  <TD>
                    <WorkloadStats w={w} />
                  </TD>
                  <TD className="font-mono text-xs text-muted-foreground">
                    {w.memory_mb ? `${w.memory_mb}MB` : '—'} / {w.cpus ?? '—'} CPU
                  </TD>
                  <TD className="text-xs text-muted-foreground">{relativeTime(w.last_seen)}</TD>
                  <TD>
                    {primaryUrl(w) ? (
                      <a
                        href={openUrl(w)}
                        target="_blank"
                        rel="noopener"
                        className="inline-flex items-center gap-1 text-xs text-accent hover:underline"
                      >
                        {primaryUrl(w).replace(/^https?:\/\//, '')}
                        {w.type === 'tinbase-project' && (
                          <span className="text-muted-foreground">(Studio)</span>
                        )}
                        <IconExternal />
                      </a>
                    ) : (
                      <span className="text-xs text-muted-foreground">—</span>
                    )}
                  </TD>
                  <TD className="text-right">
                    <div className="flex items-center justify-end gap-1.5">
                      {w.type === 'tinbase-project' && (
                        <>
                          <Button
                            size="sm"
                            variant="secondary"
                            disabled={backup.isPending}
                            title="Back up this workload's data now"
                            onClick={() => backup.mutate(w.id)}
                          >
                            <IconBackups /> {backup.isPending ? '…' : 'Backup'}
                          </Button>
                          <CopyButton value={w.service_role_key} label="service_role" />
                          <CopyButton value={w.anon_key} label="anon" />
                        </>
                      )}
                      <Button
                        size="sm"
                        variant="destructive"
                        onClick={() => {
                          if (confirm(`Delete workload "${w.name || 'primary'}"?`))
                            delWorkload.mutate(w.id)
                        }}
                      >
                        <IconTrash />
                      </Button>
                    </div>
                  </TD>
                </TR>
              ))}
            </TBody>
          </Table>
          {p && workloads.length > 0 && p.workloads.some((w) => w.type === 'tinbase-project') && (
            <p className="mt-3 text-xs text-muted-foreground">
              Sign in to a tinbase Studio with its <b className="text-foreground">service_role</b> key.
            </p>
          )}
        </CardContent>
      </Card>

      {workloads.length > 0 && (
        <Card className="mt-6">
          <CardHeader>
            <CardTitle className="text-base">Logs</CardTitle>
          </CardHeader>
          <CardContent>
            <Tabs defaultValue={workloads[0].id}>
              <TabsList>
                {workloads.map((w) => (
                  <TabsTrigger key={w.id} value={w.id}>
                    {w.name || 'primary'}
                  </TabsTrigger>
                ))}
              </TabsList>
              {workloads.map((w) => (
                <TabsContent key={w.id} value={w.id}>
                  <LogsView id={w.id} />
                </TabsContent>
              ))}
            </Tabs>
          </CardContent>
        </Card>
      )}
    </div>
  )
}
