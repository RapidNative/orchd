import { useSyncExternalStore } from 'react'

const LS = 'rnc_admin_key'

// OPEN_KEY marks "the control plane needs no key" (local dev). It is a stored
// key like any other so the app renders, but api.ts sends no Authorization for it.
export const OPEN_KEY = '__open__'
let key = localStorage.getItem(LS) || ''
const subs = new Set<() => void>()
const emit = () => subs.forEach((f) => f())

export const auth = {
  get: () => key,
  set(k: string) {
    key = k
    localStorage.setItem(LS, k)
    emit()
  },
  clear() {
    key = ''
    localStorage.removeItem(LS)
    emit()
  },
  subscribe(f: () => void) {
    subs.add(f)
    return () => {
      subs.delete(f)
    }
  },
}

export function useApiKey() {
  return useSyncExternalStore(auth.subscribe, auth.get)
}
