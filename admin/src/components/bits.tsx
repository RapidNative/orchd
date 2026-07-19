import * as React from 'react'
import { cn } from '@/lib/utils'
import { Button } from './ui/button'
import { IconCopy } from './icons'

export function PageHeader({
  title,
  subtitle,
  actions,
}: {
  title: string
  subtitle?: string
  actions?: React.ReactNode
}) {
  return (
    <div className="mb-6 flex flex-col gap-3 sm:flex-row sm:items-end sm:justify-between">
      <div className="min-w-0">
        <h1 className="text-xl font-semibold tracking-tight">{title}</h1>
        {subtitle && <p className="mt-0.5 text-sm text-muted-foreground">{subtitle}</p>}
      </div>
      {actions && (
        <div className="flex flex-wrap items-center gap-2 sm:flex-nowrap sm:justify-end">
          {actions}
        </div>
      )}
    </div>
  )
}

export function StatCard({
  label,
  value,
  sub,
  className,
}: {
  label: string
  value: React.ReactNode
  sub?: string
  className?: string
}) {
  return (
    <div className={cn('rounded-lg border border-border bg-card p-4', className)}>
      <div className="text-xs uppercase tracking-wide text-muted-foreground">{label}</div>
      <div className="mt-1 font-mono text-2xl font-semibold text-foreground">{value}</div>
      {sub && <div className="mt-0.5 text-xs text-muted-foreground">{sub}</div>}
    </div>
  )
}

export function CopyButton({ value, label }: { value?: string; label: string }) {
  const [copied, setCopied] = React.useState(false)
  if (!value) return null
  return (
    <Button
      size="sm"
      variant="secondary"
      className="font-mono"
      onClick={async () => {
        try {
          await navigator.clipboard.writeText(value)
          setCopied(true)
          setTimeout(() => setCopied(false), 1400)
        } catch {
          window.prompt('Copy:', value)
        }
      }}
    >
      <IconCopy /> {copied ? 'copied' : label}
    </Button>
  )
}
