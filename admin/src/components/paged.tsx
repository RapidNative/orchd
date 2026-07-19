import { useEffect, useMemo, useState } from 'react'
import { Input } from './ui/input'
import { Button } from './ui/button'

// usePaged filters items by a search query (via a matcher) and paginates them.
export function usePaged<T>(
  items: T[],
  match: (item: T, q: string) => boolean,
  pageSize = 10,
) {
  const [q, setQ] = useState('')
  const [page, setPage] = useState(0)

  const filtered = useMemo(() => {
    const query = q.trim().toLowerCase()
    return query ? items.filter((it) => match(it, query)) : items
  }, [items, q, match])

  useEffect(() => setPage(0), [q])

  const pageCount = Math.max(1, Math.ceil(filtered.length / pageSize))
  const clamped = Math.min(page, pageCount - 1)
  const pageItems = filtered.slice(clamped * pageSize, clamped * pageSize + pageSize)

  return { q, setQ, page: clamped, setPage, pageItems, total: filtered.length, pageCount }
}

export function SearchBox({
  value,
  onChange,
  placeholder = 'Search…',
}: {
  value: string
  onChange: (v: string) => void
  placeholder?: string
}) {
  return (
    <Input
      value={value}
      onChange={(e) => onChange(e.target.value)}
      placeholder={placeholder}
      className="h-8 max-w-56"
    />
  )
}

export function Pager({
  page,
  pageCount,
  total,
  onPage,
}: {
  page: number
  pageCount: number
  total: number
  onPage: (p: number) => void
}) {
  if (total === 0) return null
  return (
    <div className="mt-3 flex items-center justify-between px-1 text-xs text-muted-foreground">
      <span>{total} total</span>
      <div className="flex items-center gap-2">
        <Button size="sm" variant="ghost" disabled={page <= 0} onClick={() => onPage(page - 1)}>
          ← Prev
        </Button>
        <span className="font-mono">
          {page + 1} / {pageCount}
        </span>
        <Button
          size="sm"
          variant="ghost"
          disabled={page >= pageCount - 1}
          onClick={() => onPage(page + 1)}
        >
          Next →
        </Button>
      </div>
    </div>
  )
}
