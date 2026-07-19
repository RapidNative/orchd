import { PageHeader } from '@/components/bits'
import { Card, CardContent, CardHeader, CardTitle } from '@/components/ui/card'
import { Badge } from '@/components/ui/badge'

export function Backups() {
  return (
    <div>
      <PageHeader title="Backups" subtitle="Durability for project data" />
      <Card>
        <CardHeader className="flex-row items-center justify-between">
          <CardTitle className="text-base">Not configured yet</CardTitle>
          <Badge variant="warning">roadmap</Badge>
        </CardHeader>
        <CardContent>
          <p className="text-sm text-muted-foreground">
            Backups are the next item on the roadmap: scheduled <code className="font-mono">pg_dump</code>{' '}
            plus WAL archiving to S3-compatible storage (R2 / Backblaze), with restore-on-wake.
          </p>
          <p className="mt-3 text-sm text-muted-foreground">
            Until then, note that <b className="text-foreground">deleting a project is destructive</b> —
            it removes the project's on-disk volume, and there is no snapshot to restore from.
          </p>
        </CardContent>
      </Card>
    </div>
  )
}
