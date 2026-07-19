import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { PageHeader } from '@/components/bits'
import { Button } from '@/components/ui/button'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Input } from '@/components/ui/input'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { IconRefresh, IconTrash } from '@/components/icons'
import { Pager, SearchBox, usePaged } from '@/components/paged'

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
    (im, q) => im.ref.toLowerCase().includes(q) || im.id.toLowerCase().includes(q),
    12,
  )

  const regionSelect = (
    <select
      value={region}
      onChange={(e) => setRegion(e.target.value)}
      title="Region (Docker host)"
      className="h-9 rounded-md border border-border bg-input px-2 font-mono text-xs text-foreground"
    >
      <option value="">default region</option>
      {(regions.data ?? []).map((rg) => (
        <option key={rg.id} value={rg.id}>
          {rg.id}
          {rg.is_default ? ' (default)' : ''}
        </option>
      ))}
    </select>
  )

  return (
    <div>
      <PageHeader
        title="Images"
        subtitle="Docker images available to launch as workloads, per region"
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
                  <TH>Image ID</TH>
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
                    <TD className="font-mono text-xs text-muted-foreground">{im.id}</TD>
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
