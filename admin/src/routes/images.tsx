import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { PageHeader } from '@/components/bits'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { IconPlus, IconRefresh, IconTrash } from '@/components/icons'
import { Pager, SearchBox, usePaged } from '@/components/paged'
import { Select } from '@/components/ui/select'
import { BuiltImagesTable, useBuildImage } from '@/components/built-images'

// AboutImages is a short primer distinguishing the two things this page shows:
// ORCHD's versioned template freezes (tarball + docker) vs. raw daemon images.
function AboutImages() {
  return (
    <Card className="mb-4 max-w-3xl border-border/60 bg-muted/20">
      <CardContent className="space-y-2 py-4 text-sm text-muted-foreground">
        <p>
          An <span className="font-medium text-foreground">Image</span> is an immutable, versioned
          freeze of a{' '}
          <a href="/templates" className="text-primary underline-offset-2 hover:underline">
            template
          </a>
          . It has two parts: a base{' '}
          <span className="font-medium text-foreground">tarball</span> (
          <code className="font-mono text-xs">.tar.gz</code> of the source tree, stored on this
          instance — local/process boots restore from it) and a per-workspace{' '}
          <span className="font-medium text-foreground">docker image</span> tag (what the Docker
          driver runs in prod). Building always writes the tarball; docker tags are built when the
          Docker CLI is available.
        </p>
        <p>
          The card below lists these template freezes. The{' '}
          <span className="font-medium text-foreground">raw Docker images</span> table further down is
          the daemon's own image list per region — pull/remove tags directly.
        </p>
      </CardContent>
    </Card>
  )
}

// BuiltImagesCard shows immutable, versioned images frozen from templates. Each
// image is a base tarball on this instance (local boots restore from it) plus,
// per workspace, a docker image tag (prod runs them). "Build" freezes the current
// template state into the next version (v1, v2, …).
function BuiltImagesCard() {
  const templates = useQuery({ queryKey: ['templates'], queryFn: api.templates })
  const [menuOpen, setMenuOpen] = useState(false)
  const templateNames = Object.keys(templates.data ?? {})
  const build = useBuildImage()

  return (
    <Card className="mb-4">
      <CardHeader className="flex-row items-start justify-between gap-4">
        <div>
          <CardTitle className="text-base">Built from templates</CardTitle>
          <p className="mt-1 text-sm text-muted-foreground">
            Immutable, versioned freezes of a template: a base tarball (local boots from it) plus
            per-workspace docker tags (prod runs them). New projects can boot from a chosen version.
          </p>
        </div>
        <div className="relative shrink-0">
          <Button
            disabled={build.isPending || templateNames.length === 0}
            onClick={() => setMenuOpen((v) => !v)}
            title={templateNames.length === 0 ? 'Register a template first' : undefined}
          >
            <IconPlus /> {build.isPending ? 'Building…' : 'Build image'}
          </Button>
          {menuOpen && (
            <>
              <div className="fixed inset-0 z-10" onClick={() => setMenuOpen(false)} />
              <div className="absolute right-0 z-20 mt-1 w-56 overflow-hidden rounded-md border border-border bg-card py-1 shadow-lg">
                <div className="px-3 pb-1 pt-1 text-[10px] uppercase tracking-wide text-muted-foreground/70">
                  Freeze a template
                </div>
                {templateNames.map((t) => (
                  <button
                    key={t}
                    className="flex w-full items-center gap-2 px-3 py-2 text-left text-sm hover:bg-muted"
                    onClick={() => {
                      setMenuOpen(false)
                      build.mutate(t)
                    }}
                  >
                    <span className="font-mono">{t}</span>
                  </button>
                ))}
              </div>
            </>
          )}
        </div>
      </CardHeader>
      <CardContent>
        {build.isError && (
          <p className="mb-3 text-sm text-destructive">{(build.error as Error).message}</p>
        )}
        <BuiltImagesTable />
      </CardContent>
    </Card>
  )
}

// shortDigest renders "sha256:1a2b3c4d5e6f…" from a full digest for the table,
// while the full value stays available via the cell's title (for pinning/copy).
function shortDigest(d: string): string {
  const i = d.indexOf(':')
  const hex = i >= 0 ? d.slice(i + 1) : d
  const prefix = i >= 0 ? d.slice(0, i + 1) : ''
  return hex.length > 12 ? `${prefix}${hex.slice(0, 12)}…` : d
}

export function Images() {
  const qc = useQueryClient()
  const info = useQuery({ queryKey: ['info'], queryFn: api.info })
  const regions = useQuery({ queryKey: ['regions'], queryFn: api.regions })
  const [region, setRegion] = useState('')
  const [ref, setRef] = useState('')

  const supported = info.data?.images_supported
  const images = useQuery({
    queryKey: ['images', region],
    queryFn: () => api.images(region || undefined),
    enabled: supported !== false,
    refetchInterval: 15000,
  })

  const pull = useMutation({
    mutationFn: (r: string) => api.pullImage(r, region || undefined),
    onSuccess: () => {
      setRef('')
      qc.invalidateQueries({ queryKey: ['images'] })
    },
  })
  const remove = useMutation({
    mutationFn: ({ ref, force }: { ref: string; force?: boolean }) =>
      api.removeImage(ref, region || undefined, force),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['images'] }),
  })

  const paged = usePaged(
    images.data ?? [],
    (im, q) =>
      im.ref.toLowerCase().includes(q) ||
      im.id.toLowerCase().includes(q) ||
      im.digest.toLowerCase().includes(q),
    12,
  )

  const regionSelect = (
    <Select
      value={region}
      onChange={(e) => setRegion(e.target.value)}
      title="Region (Docker host)"
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
  )

  return (
    <div>
      <PageHeader
        title="Images"
        subtitle="Versioned freezes of a template, plus the raw Docker images per region"
        actions={
          <>
            {supported && (
              <SearchBox value={paged.q} onChange={paged.setQ} placeholder="Search images…" />
            )}
            {regionSelect}
            <Button variant="secondary" onClick={() => images.refetch()} title="Refresh">
              <IconRefresh />
            </Button>
          </>
        }
      />

      <AboutImages />
      <BuiltImagesCard />

      {supported === false ? (
        <Card>
          <CardHeader className="flex-row items-center justify-between">
            <CardTitle className="text-base">Not available</CardTitle>
          </CardHeader>
          <CardContent>
            <p className="text-sm text-muted-foreground">
              The active runtime driver (<code className="font-mono">{info.data?.driver}</code>)
              doesn't manage container images. Image CRUD requires the Docker driver.
            </p>
          </CardContent>
        </Card>
      ) : (
        <>
          {/* Pull */}
          <Card className="mb-4">
            <CardHeader>
              <CardTitle className="text-base">Pull an image</CardTitle>
              <p className="text-sm text-muted-foreground">
                Pull a published tag onto this region's Docker host, then use it as a workload
                image. To build a custom image, build it on the box (there is no build-upload here
                yet).
              </p>
            </CardHeader>
            <CardContent>
              <div className="flex flex-wrap items-center gap-2">
                <Input
                  value={ref}
                  onChange={(e) => setRef(e.target.value)}
                  onKeyDown={(e) => e.key === 'Enter' && ref.trim() && pull.mutate(ref.trim())}
                  placeholder="ghcr.io/acme/my-runtime:1.2.0"
                  className="max-w-md font-mono text-xs"
                />
                <Button
                  disabled={!ref.trim() || pull.isPending}
                  onClick={() => pull.mutate(ref.trim())}
                >
                  {pull.isPending ? 'Pulling…' : 'Pull'}
                </Button>
              </div>
              {pull.isError && (
                <p className="mt-2 text-sm text-destructive">{(pull.error as Error).message}</p>
              )}
              {pull.isSuccess && (
                <p className="mt-2 text-sm text-primary">Pulled {pull.data?.ref}.</p>
              )}
            </CardContent>
          </Card>

          {/* List */}
          <Card>
            <Table>
              <THead>
                <TR>
                  <TH>Repository</TH>
                  <TH>Tag</TH>
                  <TH>Digest</TH>
                  <TH>Size</TH>
                  <TH>Created</TH>
                  <TH className="text-right">Actions</TH>
                </TR>
              </THead>
              <TBody>
                {paged.pageItems.map((im) => (
                  <TR key={im.id + im.ref}>
                    <TD className="font-mono">{im.repository}</TD>
                    <TD className="font-mono text-muted-foreground">{im.tag}</TD>
                    <TD
                      className="cursor-default font-mono text-xs text-muted-foreground"
                      title={`${im.digest}\n(click-drag to copy the pinnable digest)`}
                    >
                      {shortDigest(im.digest)}
                    </TD>
                    <TD className="font-mono text-xs">{im.size}</TD>
                    <TD className="text-xs text-muted-foreground">{im.created_at}</TD>
                    <TD className="text-right">
                      <Button
                        size="sm"
                        variant="destructive"
                        disabled={remove.isPending}
                        onClick={() => {
                          if (!confirm(`Remove image ${im.ref}?`)) return
                          remove.mutate(
                            { ref: im.ref },
                            {
                              onError: (err) => {
                                // In-use images fail; offer a forced removal.
                                if (
                                  confirm(
                                    `${(err as Error).message}\n\nForce remove ${im.ref} anyway?`,
                                  )
                                )
                                  remove.mutate({ ref: im.ref, force: true })
                              },
                            },
                          )
                        }}
                      >
                        <IconTrash />
                      </Button>
                    </TD>
                  </TR>
                ))}
              </TBody>
            </Table>
            {images.data && images.data.length === 0 && (
              <p className="p-6 text-sm text-muted-foreground">
                No images on this host yet. Pull one above.
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
