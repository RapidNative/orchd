import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate } from '@tanstack/react-router'
import { api } from '@/lib/api'
import { PageHeader } from '@/components/bits'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card } from '@/components/ui/card'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { IconPlus, IconRefresh } from '@/components/icons'
import { Pager, SearchBox, usePaged } from '@/components/paged'
import { Select } from '@/components/ui/select'
import { relativeTime } from '@/lib/utils'

export function Projects() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  const projects = useQuery({ queryKey: ['projects'], queryFn: api.projects, refetchInterval: 5000 })
  const regions = useQuery({ queryKey: ['regions'], queryFn: api.regions })
  const [region, setRegion] = useState('')

  const create = useMutation({
    mutationFn: (body: Parameters<typeof api.createProject>[0]) =>
      api.createProject({ ...body, region: region || undefined }),
    onSuccess: (p) => {
      qc.invalidateQueries({ queryKey: ['projects'] })
      navigate({ to: '/projects/$id', params: { id: p.id } })
    },
  })

  const paged = usePaged(
    projects.data ?? [],
    (p, q) =>
      p.id.toLowerCase().includes(q) ||
      (p.name ?? '').toLowerCase().includes(q) ||
      (p.region ?? '').toLowerCase().includes(q),
    10,
  )

  return (
    <div>
      <PageHeader
        title="Projects"
        subtitle="Each project is one or more scale-to-zero workloads"
        actions={
          <>
            <SearchBox value={paged.q} onChange={paged.setQ} placeholder="Search projects…" />
            <Select
              value={region}
              onChange={(e) => setRegion(e.target.value)}
              title="Region for new projects"
              className="h-9 font-mono"
            >
              <option value="">default region</option>
              {(regions.data ?? []).map((rg) => (
                <option key={rg.id} value={rg.id}>
                  {rg.id}
                  {rg.is_default ? ' (default)' : ''}
                </option>
              ))}
            </Select>
            <Button
              variant="secondary"
              onClick={() => projects.refetch()}
              title="Refresh"
            >
              <IconRefresh />
            </Button>
            <Button
              variant="secondary"
              disabled={create.isPending}
              onClick={() =>
                create.mutate({
                  name: 'rapidnative',
                  workloads: [
                    { preset: 'tinbase' },
                    { preset: 'expo' },
                    { preset: 'vite' },
                    { preset: 'api' },
                  ],
                })
              }
            >
              <IconPlus /> RapidNative project
            </Button>
            <Button disabled={create.isPending} onClick={() => create.mutate({})}>
              <IconPlus /> tinbase project
            </Button>
          </>
        }
      />

      {create.isError && (
        <div className="mb-4 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {(create.error as Error).message}
        </div>
      )}

      <Card>
        <Table>
          <THead>
            <TR>
              <TH>Project</TH>
              <TH>Workloads</TH>
              <TH>Status</TH>
              <TH>Region</TH>
              <TH className="text-right">Created</TH>
            </TR>
          </THead>
          <TBody>
            {paged.pageItems.map((p) => {
              const run = p.workloads.filter((w) => w.state === 'running').length
              return (
                <TR
                  key={p.id}
                  className="cursor-pointer"
                  onClick={() => navigate({ to: '/projects/$id', params: { id: p.id } })}
                >
                  <TD>
                    <span className="font-mono">{p.id}</span>
                    {p.name && <span className="ml-2 text-xs text-muted-foreground">{p.name}</span>}
                  </TD>
                  <TD className="text-muted-foreground">{p.workloads.length}</TD>
                  <TD>
                    <Badge variant={run ? 'running' : 'suspended'}>
                      {run ? `${run} running` : 'idle'}
                    </Badge>
                  </TD>
                  <TD className="text-muted-foreground">{p.region}</TD>
                  <TD className="text-right text-xs text-muted-foreground">
                    {relativeTime(p.created_at)}
                  </TD>
                </TR>
              )
            })}
          </TBody>
        </Table>
        {projects.data && projects.data.length === 0 && (
          <p className="p-6 text-sm text-muted-foreground">
            No projects yet. Create one above.
          </p>
        )}
      </Card>
      <Pager page={paged.page} pageCount={paged.pageCount} total={paged.total} onPage={paged.setPage} />
    </div>
  )
}
