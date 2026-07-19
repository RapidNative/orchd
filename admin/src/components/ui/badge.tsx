import * as React from 'react'
import { cn } from '@/lib/utils'

const styles: Record<string, string> = {
  running: 'text-primary border-primary/40 bg-primary/10',
  suspended: 'text-muted-foreground border-border bg-muted',
  provisioning: 'text-warning border-warning/40 bg-warning/10',
  stopped: 'text-muted-foreground border-border bg-muted',
  failed: 'text-destructive border-destructive/40 bg-destructive/10',
  neutral: 'text-muted-foreground border-border bg-muted',
}

export function Badge({
  variant = 'neutral',
  className,
  ...props
}: React.HTMLAttributes<HTMLSpanElement> & { variant?: keyof typeof styles }) {
  return (
    <span
      className={cn(
        'inline-flex items-center rounded-full border px-2.5 py-0.5 font-mono text-xs',
        styles[variant] ?? styles.neutral,
        className,
      )}
      {...props}
    />
  )
}
