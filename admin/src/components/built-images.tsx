import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { relativeTime } from '@/lib/utils'

// useBuildImage is the shared "freeze a template into the next version" mutation,
// used by the Build controls on both the Images and Templates pages.
export function useBuildImage() {
  const qc = useQueryClient()
  return useMutation({
    mutationFn: (name: string) => api.buildImage(name),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['built-images'] }),
  })
}

// BuiltImagesTable renders the frozen template images (tarball + per-workspace
// docker tags) with a delete action. Pass `filterTemplate` to scope it to one
// template (used on the Templates page rows); omit for the full list.
export function BuiltImagesTable({ filterTemplate }: { filterTemplate?: string }) {
  const qc = useQueryClient()
  const built = useQuery({
    queryKey: ['built-images'],
    queryFn: api.builtImages,
    refetchInterval: 10000,
  })
  const del = useMutation({
    mutationFn: ({ template, version }: { template: string; version: string }) =>
      api.deleteBuiltImage(template, version),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['built-images'] }),
  })

  const images = (built.data ?? []).filter((im) => !filterTemplate || im.template === filterTemplate)

  if (images.length === 0) {
    return (
      <p className="text-sm text-muted-foreground">
        {filterTemplate
          ? 'No versions built from this template yet.'
          : 'No images built yet. Build one from a registered template.'}
      </p>
    )
  }

  return (
    <Table>
      <THead>
        <TR>
          {!filterTemplate && <TH>Template</TH>}
          <TH>Version</TH>
          <TH>Docker tags</TH>
          <TH>Built</TH>
          <TH className="text-right">Actions</TH>
        </TR>
      </THead>
      <TBody>
        {images.map((im) => {
          const tags = Object.entries(im.dockers ?? {})
          return (
            <TR key={im.template + '@' + im.version}>
              {!filterTemplate && <TD className="font-mono">{im.template}</TD>}
              <TD>
                <Badge variant="running">{im.version}</Badge>
              </TD>
              <TD className="text-xs text-muted-foreground">
                {tags.length === 0 ? (
                  <span title={im.tarball}>tarball only</span>
                ) : (
                  <div className="flex flex-col gap-0.5">
                    {tags.map(([ws, tag]) => (
                      <span key={ws} className="font-mono">
                        {ws}: {tag}
                      </span>
                    ))}
                  </div>
                )}
              </TD>
              <TD className="text-xs text-muted-foreground">{relativeTime(im.created_at)}</TD>
              <TD className="text-right">
                <Button
                  size="sm"
                  variant="destructive"
                  disabled={del.isPending}
                  onClick={() => {
                    if (!confirm(`Delete image ${im.template}@${im.version}?`)) return
                    del.mutate({ template: im.template, version: im.version })
                  }}
                >
                  Delete
                </Button>
              </TD>
            </TR>
          )
        })}
      </TBody>
    </Table>
  )
}
