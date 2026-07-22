import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { PageHeader } from '@/components/bits'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { IconTrash } from '@/components/icons'

export function Templates() {
  return (
    <div>
      <PageHeader
        title="Templates"
        subtitle="Registered project templates — the live, mutable source a project is built from"
      />
      <AboutTemplates />
      <TemplatesCard />
    </div>
  )
}

// AboutTemplates is a short primer so the page is self-explanatory: what a
// template is, and how it relates to Images (tarball vs docker).
function AboutTemplates() {
  return (
    <Card className="mb-6 max-w-3xl border-border/60 bg-muted/20">
      <CardContent className="space-y-2 py-4 text-sm text-muted-foreground">
        <p>
          A <span className="font-medium text-foreground">template</span> is a project folder (a
          monorepo) with an <code className="font-mono text-xs">orchd.json</code> at its root. It
          lists the <span className="font-medium text-foreground">workloads</span> a project runs —
          each a workspace of kind <code className="font-mono text-xs">tinbase</code>,{' '}
          <code className="font-mono text-xs">node</code>, or{' '}
          <code className="font-mono text-xs">static</code>, on its own port (local) or image (prod).
        </p>
        <p>
          Templates are <span className="font-medium text-foreground">live and mutable</span>. To
          freeze one for reuse, build an{' '}
          <a href="/images" className="text-primary underline-offset-2 hover:underline">
            Image
          </a>{' '}
          from it: a base <span className="font-medium text-foreground">tarball</span> (local boots
          restore from it) plus a per-workspace <span className="font-medium text-foreground">docker
          image</span> tag (prod runs it). Create a project from a template on the{' '}
          <a href="/projects" className="text-primary underline-offset-2 hover:underline">
            Projects
          </a>{' '}
          page; provisioning is async.
        </p>
      </CardContent>
    </Card>
  )
}

function TemplatesCard() {
  const qc = useQueryClient()
  const templates = useQuery({ queryKey: ['templates'], queryFn: api.templates })
  const [name, setName] = useState('')
  const [path, setPath] = useState('')
  const inval = () => qc.invalidateQueries({ queryKey: ['templates'] })
  const save = useMutation({
    mutationFn: () => api.setTemplate(name.trim(), path.trim()),
    onSuccess: () => {
      setName('')
      setPath('')
      inval()
    },
  })
  const del = useMutation({ mutationFn: (n: string) => api.setTemplate(n, ''), onSuccess: inval })
  const [browse, setBrowse] = useState<string | null>(null)
  const entries = Object.entries(templates.data ?? {})

  return (
    <Card className="mb-6 max-w-3xl">
      <CardHeader>
        <CardTitle className="text-base">Registered templates</CardTitle>
      </CardHeader>
      <CardContent>
        <Table>
          <THead>
            <TR>
              <TH>Name</TH>
              <TH>Local path</TH>
              <TH></TH>
            </TR>
          </THead>
          <TBody>
            {entries.map(([n, p]) => (
              <TR key={n}>
                <TD className="font-mono">{n}</TD>
                <TD className="font-mono text-xs text-muted-foreground">{p}</TD>
                <TD className="text-right">
                  <div className="flex items-center justify-end gap-1.5">
                    <Button size="sm" variant="secondary" onClick={() => setBrowse(n)}>
                      Browse
                    </Button>
                    <Button size="sm" variant="destructive" onClick={() => del.mutate(n)}>
                      <IconTrash />
                    </Button>
                  </div>
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
        {entries.length === 0 && (
          <p className="py-3 text-sm text-muted-foreground">
            No templates registered. Add one below (or drop a folder into{' '}
            <code className="font-mono text-xs">template-examples/</code>, which auto-registers on
            startup).
          </p>
        )}
        <div className="mt-3 flex flex-wrap items-end gap-2">
          <Input
            className="max-w-40"
            placeholder="name (e.g. rapidnative)"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Input
            className="max-w-96 flex-1"
            placeholder="/absolute/path/to/template (has orchd.json)"
            value={path}
            onChange={(e) => setPath(e.target.value)}
          />
          <Button onClick={() => save.mutate()} disabled={save.isPending || !name.trim() || !path.trim()}>
            Add template
          </Button>
          {save.isError && <span className="text-sm text-destructive">{(save.error as Error).message}</span>}
        </div>
        <p className="mt-2 text-xs text-muted-foreground">
          A local folder with an <code className="font-mono">orchd.json</code>. Create a project from
          it on the Projects page — each workspace runs on its own port. Freeze it into a versioned
          Image from the Images page.
        </p>
      </CardContent>
      {browse && <TemplateBrowser name={browse} onClose={() => setBrowse(null)} />}
    </Card>
  )
}

// TemplateBrowser shows a template's declared workspace images + base bundle URL
// and lets the base files be browsed read-only.
function TemplateBrowser({ name, onClose }: { name: string; onClose: () => void }) {
  const man = useQuery({ queryKey: ['tmpl', name], queryFn: () => api.template(name) })
  const files = useQuery({ queryKey: ['tmplfiles', name], queryFn: () => api.templateFiles(name) })
  const [sel, setSel] = useState<string | null>(null)
  const [content, setContent] = useState('')
  const openFile = useMutation({
    mutationFn: (p: string) => api.templateFile(name, p),
    onSuccess: (c, p) => {
      setSel(p)
      setContent(c)
    },
  })
  const images = (man.data?.workloads ?? []).filter((w) => w.image).map((w) => w.image as string)
  return (
    <div className="fixed inset-0 z-50 grid place-items-center bg-black/60 p-4" onClick={onClose}>
      <Card className="flex h-[80vh] w-full max-w-4xl flex-col" onClick={(e) => e.stopPropagation()}>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-base">Template — {name}</CardTitle>
          <Button size="sm" variant="ghost" onClick={onClose}>
            Close
          </Button>
        </CardHeader>
        <CardContent className="flex min-h-0 flex-1 flex-col gap-3">
          <div className="flex flex-wrap items-center gap-2 text-xs">
            <span className="text-muted-foreground">Declared images:</span>
            {images.length ? (
              images.map((i) => (
                <Badge key={i} variant="neutral">
                  {i}
                </Badge>
              ))
            ) : (
              <span className="text-muted-foreground">none</span>
            )}
            <span className="ml-2 text-muted-foreground">Bundle:</span>
            <code className="font-mono text-[11px] text-muted-foreground">
              {api.templateBundleUrl(name)}
            </code>
          </div>
          <div className="grid min-h-0 flex-1 gap-3 md:grid-cols-[260px_1fr]">
            <div className="overflow-auto rounded-md border border-border">
              {(files.data ?? []).map((f) => (
                <button
                  key={f}
                  onClick={() => openFile.mutate(f)}
                  className={
                    'block w-full truncate px-2 py-1 text-left font-mono text-xs hover:bg-muted ' +
                    (sel === f ? 'bg-muted text-foreground' : 'text-muted-foreground')
                  }
                >
                  {f}
                </button>
              ))}
            </div>
            <pre className="overflow-auto rounded-md border border-border bg-[#080b12] p-3 font-mono text-xs text-muted-foreground">
              {sel ? content : 'Select a file to view.'}
            </pre>
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
