import { useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { BuiltImage, ImageImportSpec } from '@/lib/types'
import { Badge } from '@/components/ui/badge'
import { Button } from '@/components/ui/button'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { IconCopy } from '@/components/icons'
import { relativeTime } from '@/lib/utils'

// importSpecFor builds the paste-on-another-instance descriptor from an image,
// using its registry refs (pullable elsewhere) and frozen workload shape. Done
// client-side so copying is instant and offline — no server round-trip.
function importSpecFor(im: BuiltImage): ImageImportSpec {
  return {
    template: im.template,
    version: im.version,
    dockers: im.registry ?? {},
    workloads: im.workloads ?? [],
  }
}

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
  const settings = useQuery({ queryKey: ['settings'], queryFn: api.settings })
  const registrySet = !!settings.data?.registry
  const [note, setNote] = useState<string>('')

  const del = useMutation({
    mutationFn: ({ template, version }: { template: string; version: string }) =>
      api.deleteBuiltImage(template, version),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['built-images'] }),
  })
  const push = useMutation({
    mutationFn: ({ template, version }: { template: string; version: string }) =>
      api.pushImage(template, version),
    onSuccess: () => {
      setNote('Pushed to registry.')
      qc.invalidateQueries({ queryKey: ['built-images'] })
    },
    onError: (e) => setNote((e as Error).message),
  })
  // Copy the import spec (registry refs + workload shape) for pasting on another
  // ORCHD instance's "Import image" form. Built from the image itself.
  const copySpec = async (im: BuiltImage) => {
    const json = JSON.stringify(importSpecFor(im), null, 2)
    try {
      await navigator.clipboard.writeText(json)
      setNote(`Copied import spec for ${im.template}@${im.version}.`)
    } catch {
      setNote('Clipboard blocked — spec logged to the console instead.')
      console.log(json)
    }
  }

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
    <div className="flex flex-col gap-2">
      {note && <p className="text-xs text-muted-foreground">{note}</p>}
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
            const pushed = !!im.registry && Object.keys(im.registry).length > 0
            const pushingThis =
              push.isPending && push.variables?.template === im.template && push.variables?.version === im.version
            return (
              <TR key={im.template + '@' + im.version}>
                {!filterTemplate && <TD className="font-mono">{im.template}</TD>}
                <TD>
                  <div className="flex items-center gap-1.5">
                    <Badge variant="running">{im.version}</Badge>
                    {im.imported && <Badge variant="neutral">imported</Badge>}
                    {pushed && <Badge variant="neutral">pushed</Badge>}
                  </div>
                </TD>
                <TD className="text-xs text-muted-foreground">
                  {tags.length === 0 ? (
                    <span title={im.tarball}>{im.imported ? 'docker-only' : 'tarball only'}</span>
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
                  <div className="flex items-center justify-end gap-1.5">
                    {tags.length > 0 && !im.imported && (
                      <Button
                        size="sm"
                        variant="secondary"
                        disabled={push.isPending || !registrySet}
                        title={registrySet ? 'Re-tag & push to the configured registry' : 'Set a registry in Settings first'}
                        onClick={() => push.mutate({ template: im.template, version: im.version })}
                      >
                        {pushingThis ? 'Pushing…' : 'Push'}
                      </Button>
                    )}
                    {pushed && (
                      <Button
                        size="sm"
                        variant="secondary"
                        title="Copy the import JSON to paste on another ORCHD instance"
                        onClick={() => copySpec(im)}
                      >
                        <IconCopy /> Copy spec
                      </Button>
                    )}
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
                  </div>
                </TD>
              </TR>
            )
          })}
        </TBody>
      </Table>
    </div>
  )
}

// ImportImageCard is the target-side form: paste an import spec copied from
// another instance's pushed image, and register it here (docker-only) so
// projects can boot from it.
export function ImportImageCard() {
  const qc = useQueryClient()
  const [text, setText] = useState('')
  const importImg = useMutation({
    mutationFn: () => api.importImage(JSON.parse(text)),
    onSuccess: () => {
      setText('')
      qc.invalidateQueries({ queryKey: ['built-images'] })
    },
  })
  let parseError = ''
  if (text.trim()) {
    try {
      JSON.parse(text)
    } catch {
      parseError = 'Not valid JSON yet.'
    }
  }
  return (
    <div className="flex flex-col gap-2">
      <p className="text-sm text-muted-foreground">
        Paste an import spec copied from another instance (its image must be pushed to a registry
        this instance can pull from). Registers the image docker-only so projects here can boot it.
      </p>
      <textarea
        className="h-32 w-full rounded-md border border-border bg-input p-2 font-mono text-xs text-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
        placeholder='{ "template": "rapidnative", "version": "v2", "dockers": { "api": "ghcr.io/acme/orchd-rapidnative-api:v2" }, "workloads": [ … ] }'
        value={text}
        onChange={(e) => setText(e.target.value)}
      />
      <div className="flex items-center gap-3">
        <Button
          disabled={!text.trim() || !!parseError || importImg.isPending}
          onClick={() => importImg.mutate()}
        >
          {importImg.isPending ? 'Importing…' : 'Import image'}
        </Button>
        {parseError && <span className="text-xs text-muted-foreground">{parseError}</span>}
        {importImg.isError && (
          <span className="text-sm text-destructive">{(importImg.error as Error).message}</span>
        )}
        {importImg.isSuccess && <span className="text-sm text-primary">Imported.</span>}
      </div>
    </div>
  )
}
