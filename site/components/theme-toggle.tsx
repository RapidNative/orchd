'use client'

import { useEffect, useState } from 'react'

// The inline script in the root layout has already set the class before paint;
// this only mirrors it into React state and lets the user flip it.
export function ThemeToggle() {
  const [dark, setDark] = useState<boolean | null>(null)

  useEffect(() => {
    setDark(document.documentElement.classList.contains('dark'))
  }, [])

  function toggle() {
    const next = !document.documentElement.classList.contains('dark')
    document.documentElement.classList.toggle('dark', next)
    try {
      localStorage.setItem('orchd-theme', next ? 'dark' : 'light')
    } catch {}
    setDark(next)
  }

  return (
    <button
      type="button"
      onClick={toggle}
      aria-label="Toggle colour theme"
      className="grid h-8 w-8 place-items-center rounded-md border border-line text-dim transition hover:border-accent hover:text-accent"
    >
      <svg viewBox="0 0 24 24" width="15" height="15" aria-hidden="true">
        {dark ? (
          <path
            fill="none"
            d="M12 4V2m0 20v-2m8-8h2M2 12h2m13.66-5.66 1.41-1.41M4.93 19.07l1.41-1.41m11.32 0 1.41 1.41M4.93 4.93l1.41 1.41M16 12a4 4 0 1 1-8 0 4 4 0 0 1 8 0Z"
            stroke="currentColor"
            strokeWidth="1.6"
            strokeLinecap="round"
          />
        ) : (
          <path
            fill="currentColor"
            d="M20 13.5A8.5 8.5 0 0 1 10.5 4a8.5 8.5 0 1 0 9.5 9.5Z"
          />
        )}
      </svg>
    </button>
  )
}
