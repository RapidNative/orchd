import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { useNavigate, useSearch } from '@tanstack/react-router'
import { api } from '@/lib/api'
import { PageHeader } from '@/components/bits'
import { Button } from '@/components/ui/button'
import { Badge } from '@/components/ui/badge'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { IconPlus, IconRefresh, IconTrash } from '@/components/icons'
import { Pager, SearchBox } from '@/components/paged'
import { Select } from '@/components/ui/select'
import { relativeTime } from '@/lib/utils'

const PAGE_SIZE = 20

export function Projects() {
  const qc = useQueryClient()
  const navigate = useNavigate()
  // Pagination + search live in the URL (?page=&q=), so pages are shareable and
  // the browser back button works. The server does the paging/sorting.
  const { page, q } = useSearch({ from: '/projects' })
  const setSearch = (next: Partial<{ page: number; q: string }>) =>
    navigate({ to: '/projects', search: (prev) => ({ ...prev, ...next }), replace: true })

  // Debounce the search input into the URL so typing doesn't refetch per keystroke.
  const [qInput, setQInput] = useState(q)
  useEffect(() => setQInput(q), [q])
  useEffect(() => {
    if (qInput === q) return
    const t = setTimeout(() => setSearch({ q: qInput, page: 0 }), 300)
    return () => clearTimeout(t)
  }, [qInput]) // eslint-disable-line react-hooks/exhaustive-deps

  const projects = useQuery({
    queryKey: ['projects', 'page', page, q],
    queryFn: () => api.projectsPage({ page, pageSize: PAGE_SIZE, q, sort: 'recent' }),
    refetchInterval: 5000,
  })
  const items = projects.data?.items ?? []
  const total = projects.data?.total ?? 0
  const pageCount = Math.max(1, Math.ceil(total / PAGE_SIZE))

  const regions = useQuery({ queryKey: ['regions'], queryFn: api.regions })
  const templates = useQuery({ queryKey: ['templates'], queryFn: api.templates })
  const builtImages = useQuery({ queryKey: ['built-images'], queryFn: api.builtImages })
  const [region, setRegion] = useState('')
  const [menuOpen, setMenuOpen] = useState(false)
  const [createTmpl, setCreateTmpl] = useState<string | null>(null)
  const templateNames = Object.keys(templates.data ?? {})
  const images = builtImages.data ?? []

  const create = useMutation({
    mutationFn: (body: Parameters<typeof api.createProject>[0]) =>
      api.createProject({ ...body, region: region || undefined }),
    onSuccess: (p) => {
      qc.invalidateQueries({ queryKey: ['projects'] })
      navigate({ to: '/projects/$id', params: { id: p.id } })
    },
  })

  return (
    <div>
      <PageHeader
        title="Projects"
        subtitle="Each project is one or more scale-to-zero workloads"
        actions={
          <>
            <SearchBox value={qInput} onChange={setQInput} placeholder="Search projects…" />
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
            <Button variant="secondary" onClick={() => projects.refetch()} title="Refresh">
              <IconRefresh />
            </Button>
            <div className="relative">
              <Button disabled={create.isPending} onClick={() => setMenuOpen((v) => !v)}>
                <IconPlus /> New project
              </Button>
              {menuOpen && (
                <>
                  <div className="fixed inset-0 z-10" onClick={() => setMenuOpen(false)} />
                  <div className="absolute right-0 z-20 mt-1 w-60 overflow-hidden rounded-md border border-border bg-card py-1 shadow-lg">
                    {templateNames.length > 0 && (
                      <div className="px-3 pb-1 pt-1 text-[10px] uppercase tracking-wide text-muted-foreground/70">
                        From template
                      </div>
                    )}
                    {templateNames.map((t) => (
                      <button
                        key={t}
                        className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-muted"
                        onClick={() => {
                          setMenuOpen(false)
                          setCreateTmpl(t)
                        }}
                      >
                        <span className="font-mono">{t}</span>
                      </button>
                    ))}
                    {images.length > 0 && (
                      <>
                        <div className="my-1 border-t border-border" />
                        <div className="px-3 pb-1 pt-1 text-[10px] uppercase tracking-wide text-muted-foreground/70">
                          From image (frozen)
                        </div>
                        {images.map((im) => (
                          <button
                            key={im.template + '@' + im.version}
                            className="flex w-full items-center justify-between gap-2 px-3 py-2 text-left text-sm hover:bg-muted"
                            onClick={() => {
                              setMenuOpen(false)
                              create.mutate({ image: im.template + '@' + im.version })
                            }}
                          >
                            <span className="font-mono">{im.template}</span>
                            <span className="text-xs text-muted-foreground">{im.version}</span>
                          </button>
                        ))}
                      </>
                    )}
                    <div className="my-1 border-t border-border" />
                    <button
                      className="flex w-full items-center px-3 py-2 text-left text-sm text-muted-foreground hover:bg-muted hover:text-foreground"
                      onClick={() => {
                        setMenuOpen(false)
                        create.mutate({})
                      }}
                    >
                      Blank (tinbase backend)
                    </button>
                  </div>
                </>
              )}
            </div>
          </>
        }
      />

      {create.isError && (
        <div className="mb-4 rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
          {(create.error as Error).message}
        </div>
      )}

      {createTmpl && (
        <CreateDialog
          template={createTmpl}
          pending={create.isPending}
          onCancel={() => setCreateTmpl(null)}
          onCreate={(delta) => create.mutate({ template: createTmpl, delta })}
        />
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
            {items.map((p) => {
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
        {!projects.isLoading && total === 0 && (
          <p className="p-6 text-sm text-muted-foreground">
            {q ? `No projects match “${q}”.` : 'No projects yet. Create one above.'}
          </p>
        )}
      </Card>
      <Pager page={page} pageCount={pageCount} total={total} onPage={(p) => setSearch({ page: p })} />
    </div>
  )
}

// CreateDialog creates a project from a template, optionally overlaying a delta
// (files patched on top of the base) — the same base+delta model RapidNative uses.
function CreateDialog({
  template,
  pending,
  onCancel,
  onCreate,
}: {
  template: string
  pending: boolean
  onCancel: () => void
  onCreate: (delta: Record<string, string>) => void
}) {
  const [rows, setRows] = useState<{ path: string; content: string }[]>([])
  const buildDelta = () => {
    const d: Record<string, string> = {}
    for (const r of rows) if (r.path.trim()) d[r.path.trim()] = r.content
    return d
  }
  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/60 p-4" onClick={onCancel}>
      <Card className="w-full max-w-2xl" onClick={(e) => e.stopPropagation()}>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-base">New project from “{template}”</CardTitle>
          <Button size="sm" variant="ghost" onClick={onCancel}>
            Close
          </Button>
        </CardHeader>
        <CardContent>
          <p className="mb-3 text-sm text-muted-foreground">
            Optionally overlay files on the base (a delta). Leave empty for the plain template.
          </p>
          <div className="flex flex-col gap-3">
            {rows.map((r, i) => (
              <div key={i} className="rounded-md border border-border p-2">
                <div className="flex items-center gap-2">
                  <Input
                    className="font-mono text-xs"
                    placeholder="path e.g. api/index.js"
                    value={r.path}
                    onChange={(e) =>
                      setRows((rs) => rs.map((x, j) => (j === i ? { ...x, path: e.target.value } : x)))
                    }
                  />
                  <Button
                    size="sm"
                    variant="ghost"
                    onClick={() => setRows((rs) => rs.filter((_, j) => j !== i))}
                  >
                    <IconTrash />
                  </Button>
                </div>
                <textarea
                  className="mt-2 h-24 w-full rounded-md border border-border bg-input p-2 font-mono text-xs text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
                  placeholder="file content"
                  value={r.content}
                  onChange={(e) =>
                    setRows((rs) => rs.map((x, j) => (j === i ? { ...x, content: e.target.value } : x)))
                  }
                />
              </div>
            ))}
            <div className="flex items-center justify-between">
              <Button
                size="sm"
                variant="secondary"
                onClick={() => setRows((rs) => [...rs, { path: '', content: '' }])}
              >
                <IconPlus /> Add file
              </Button>
              <Button disabled={pending} onClick={() => onCreate(buildDelta())}>
                {pending ? 'Creating…' : 'Create'}
              </Button>
            </div>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
