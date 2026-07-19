import * as React from 'react'
import { cn } from '@/lib/utils'

// A styled native <select>. We hide the platform chevron (appearance-none) and
// draw our own with proper right padding, so the arrow never hugs the edge the
// way the default control does.
export const Select = React.forwardRef<
  HTMLSelectElement,
  React.SelectHTMLAttributes<HTMLSelectElement>
>(({ className, children, ...props }, ref) => (
  <div className="relative inline-flex w-fit shrink-0">
    <select
      ref={ref}
      className={cn(
        'h-8 w-full appearance-none rounded-md border border-border bg-input pl-2.5 pr-8 text-xs text-foreground',
        'focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring',
        'disabled:cursor-not-allowed disabled:opacity-50',
        className,
      )}
      {...props}
    >
      {children}
    </select>
    <svg
      viewBox="0 0 24 24"
      className="pointer-events-none absolute right-2 top-1/2 size-4 -translate-y-1/2 text-muted-foreground"
      fill="none"
      stroke="currentColor"
      strokeWidth="2"
      strokeLinecap="round"
      strokeLinejoin="round"
      aria-hidden="true"
    >
      <path d="m6 9 6 6 6-6" />
    </svg>
  </div>
))
Select.displayName = 'Select'
