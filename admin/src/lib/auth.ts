import { useSyncExternalStore } from 'react'

const LS = 'rnc_admin_key'
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
