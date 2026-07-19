import type { ReactNode } from 'react'
import { useEffect, useState } from 'react'
import { useMutation, useQuery, useQueryClient } from '@tanstack/react-query'
import { api } from '@/lib/api'
import type { BackupTarget } from '@/lib/types'
import { PageHeader } from '@/components/bits'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Button } from '@/components/ui/button'
import { Input } from '@/components/ui/input'
import { Badge } from '@/components/ui/badge'
import { Table, TBody, TD, TH, THead, TR } from '@/components/ui/table'
import { IconTrash } from '@/components/icons'
import { api as API } from '@/lib/api'

function RegionsCard() {
  const qc = useQueryClient()
  const regions = useQuery({ queryKey: ['regions'], queryFn: API.regions })
  const [name, setName] = useState('')
  const [host, setHost] = useState('')
  const inval = () => qc.invalidateQueries({ queryKey: ['regions'] })
  const add = useMutation({
    mutationFn: () => API.createRegion(name, host || undefined),
    onSuccess: () => {
      setName('')
      setHost('')
      inval()
    },
  })
  const del = useMutation({ mutationFn: (id: string) => API.deleteRegion(id), onSuccess: inval })
  const setDefault = useMutation({
    mutationFn: (id: string) => API.setDefaultRegion(id),
    onSuccess: inval,
  })

  return (
    <Card className="mb-6 max-w-2xl">
      <CardHeader>
        <CardTitle className="text-base">Regions</CardTitle>
      </CardHeader>
      <CardContent>
        <Table>
          <THead>
            <TR>
              <TH>Region</TH>
              <TH>Docker host</TH>
              <TH></TH>
              <TH></TH>
            </TR>
          </THead>
          <TBody>
            {(regions.data ?? []).map((rg) => (
              <TR key={rg.id}>
                <TD>
                  <span className="font-mono">{rg.id}</span>
                  {rg.is_default && (
                    <Badge variant="running" className="ml-2">
                      default
                    </Badge>
                  )}
                </TD>
                <TD className="font-mono text-xs text-muted-foreground">
                  {rg.docker_host || 'local'}
                </TD>
                <TD className="text-right">
                  {!rg.is_default && (
                    <Button size="sm" variant="ghost" onClick={() => setDefault.mutate(rg.id)}>
                      Set default
                    </Button>
                  )}
                </TD>
                <TD className="text-right">
                  <Button
                    size="sm"
                    variant="destructive"
                    disabled={rg.is_default}
                    title={rg.is_default ? 'set another default first' : 'delete region'}
                    onClick={() => del.mutate(rg.id)}
                  >
                    <IconTrash />
                  </Button>
                </TD>
              </TR>
            ))}
          </TBody>
        </Table>
        <div className="mt-3 flex flex-wrap items-end gap-2">
          <Input
            className="max-w-40"
            placeholder="name (e.g. eu-west)"
            value={name}
            onChange={(e) => setName(e.target.value)}
          />
          <Input
            className="max-w-64"
            placeholder="docker host (optional, e.g. ssh://root@node2)"
            value={host}
            onChange={(e) => setHost(e.target.value)}
          />
          <Button onClick={() => add.mutate()} disabled={add.isPending || !name.trim()}>
            Add region
          </Button>
          {add.isError && <span className="text-sm text-destructive">{(add.error as Error).message}</span>}
        </div>
        <p className="mt-2 text-xs text-muted-foreground">
          A region maps to a data plane. Today all run on this box; a docker host points a region at
          a remote worker node (the seam for multi-node).
        </p>
      </CardContent>
    </Card>
  )
}

function Field({ label, children, hint }: { label: string; children: ReactNode; hint?: string }) {
  return (
    <label className="grid gap-1.5">
      <span className="text-sm text-muted-foreground">{label}</span>
      {children}
      {hint && <span className="text-xs text-muted-foreground/70">{hint}</span>}
    </label>
  )
}

export function Settings() {
  const qc = useQueryClient()
  const settings = useQuery({ queryKey: ['settings'], queryFn: api.settings })

  const [t, setT] = useState<BackupTarget>({ type: 'local' })
  const [secret, setSecret] = useState('')
  const [webhook, setWebhook] = useState('')
  const secretSet = settings.data?.backup_secret_set

  useEffect(() => {
    if (settings.data) {
      setT({ ...settings.data.backup, type: settings.data.backup.type || 'local' })
      setWebhook(settings.data.webhook?.url ?? '')
    }
  }, [settings.data])

  const saveWebhook = useMutation({
    mutationFn: () => api.setWebhook(webhook),
    onSuccess: () => qc.invalidateQueries({ queryKey: ['settings'] }),
  })

  const save = useMutation({
    mutationFn: () => api.setBackupTarget({ ...t, secret_key: secret || undefined }),
    onSuccess: () => {
      setSecret('')
      qc.invalidateQueries({ queryKey: ['settings'] })
      qc.invalidateQueries({ queryKey: ['info'] })
      qc.invalidateQueries({ queryKey: ['backups'] })
    },
  })

  const set = (k: keyof BackupTarget, v: string) => setT((p) => ({ ...p, [k]: v }))

  return (
    <div>
      <PageHeader title="Settings" subtitle="Regions, backups, notifications" />

      <RegionsCard />

      <Card className="max-w-2xl">
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-base">Backup target</CardTitle>
          <Badge variant={t.type === 's3' ? 'running' : 'neutral'}>{t.type}</Badge>
        </CardHeader>
        <CardContent className="grid gap-4">
          <Field label="Destination">
            <select
              value={t.type}
              onChange={(e) => set('type', e.target.value)}
              className="h-9 rounded-md border border-border bg-input px-2 font-mono text-sm text-foreground"
            >
              <option value="local">local (on the box)</option>
              <option value="s3">s3 / R2 / B2 / MinIO</option>
            </select>
          </Field>

          {t.type === 's3' && (
            <>
              <Field label="Endpoint" hint="e.g. https://<acct>.r2.cloudflarestorage.com or http://127.0.0.1:9000">
                <Input
                  value={t.endpoint ?? ''}
                  onChange={(e) => set('endpoint', e.target.value)}
                  placeholder="https://…"
                />
              </Field>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Bucket">
                  <Input value={t.bucket ?? ''} onChange={(e) => set('bucket', e.target.value)} />
                </Field>
                <Field label="Region" hint="R2: auto · MinIO: us-east-1">
                  <Input value={t.region ?? ''} onChange={(e) => set('region', e.target.value)} placeholder="auto" />
                </Field>
              </div>
              <Field label="Key prefix" hint="optional (default: backups)">
                <Input value={t.prefix ?? ''} onChange={(e) => set('prefix', e.target.value)} placeholder="backups" />
              </Field>
              <div className="grid gap-4 sm:grid-cols-2">
                <Field label="Access key">
                  <Input value={t.access_key ?? ''} onChange={(e) => set('access_key', e.target.value)} />
                </Field>
                <Field
                  label="Secret key"
                  hint={secretSet ? 'a secret is set — leave blank to keep it' : 'required'}
                >
                  <Input
                    type="password"
                    value={secret}
                    onChange={(e) => setSecret(e.target.value)}
                    placeholder={secretSet ? '•••••••• (unchanged)' : ''}
                  />
                </Field>
              </div>
            </>
          )}

          {save.isError && (
            <div className="rounded-md border border-destructive/40 bg-destructive/10 px-3 py-2 text-sm text-destructive">
              {(save.error as Error).message}
            </div>
          )}

          <div className="flex items-center gap-3">
            <Button onClick={() => save.mutate()} disabled={save.isPending}>
              {save.isPending ? 'Saving…' : 'Save & apply'}
            </Button>
            {save.isSuccess && <span className="text-sm text-primary">Saved.</span>}
          </div>
          <p className="text-xs text-muted-foreground">
            Saving validates the target and switches the live backup store — new backups go here
            immediately. The secret is stored on the orchestrator and never returned by the API.
          </p>
        </CardContent>
      </Card>

      <Card className="mt-6 max-w-2xl">
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-base">Event webhook</CardTitle>
          <Badge variant={webhook ? 'running' : 'neutral'}>{webhook ? 'on' : 'off'}</Badge>
        </CardHeader>
        <CardContent className="grid gap-4">
          <Field label="Webhook URL" hint="control-plane events (project/backup/restore …) are POSTed here as JSON. Blank = off.">
            <Input
              value={webhook}
              onChange={(e) => setWebhook(e.target.value)}
              placeholder="https://example.com/hooks/tinbase"
            />
          </Field>
          <div className="flex items-center gap-3">
            <Button onClick={() => saveWebhook.mutate()} disabled={saveWebhook.isPending}>
              {saveWebhook.isPending ? 'Saving…' : 'Save'}
            </Button>
            {saveWebhook.isSuccess && <span className="text-sm text-primary">Saved.</span>}
          </div>
        </CardContent>
      </Card>
    </div>
  )
}
