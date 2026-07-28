import { useEffect, useState } from 'react'
import { auth, OPEN_KEY } from '@/lib/auth'
import { Button } from './ui/button'
import { Input } from './ui/input'
import { Card, CardContent, CardDescription, CardHeader, CardTitle } from './ui/card'
import { BrandLogo } from './icons'

const BASE = import.meta.env.VITE_API_BASE ?? '/api'

// useOpenApi probes the control plane with no credentials. A 200 means it runs
// without an API key (local dev), so there is nothing to gate on and we connect
// straight through. Returns null while the probe is in flight.
function useOpenApi() {
  const [open, setOpen] = useState<boolean | null>(null)
  useEffect(() => {
    let live = true
    fetch(BASE + '/v1/projects')
      .then((r) => live && setOpen(r.ok))
      .catch(() => live && setOpen(false))
    return () => {
      live = false
    }
  }, [])
  return open
}

export function KeyGate() {
  const [val, setVal] = useState('')
  const [err, setErr] = useState('')
  const [busy, setBusy] = useState(false)
  const openApi = useOpenApi()

  // Unauthenticated control plane: skip the gate entirely.
  useEffect(() => {
    if (openApi) auth.set(OPEN_KEY)
  }, [openApi])

  if (openApi === null || openApi) return null

  async function connect() {
    const k = val.trim()
    if (!k) {
      setErr('Enter a key')
      return
    }
    setBusy(true)
    setErr('')
    try {
      const res = await fetch(BASE + '/v1/projects', { headers: { Authorization: 'Bearer ' + k } })
      if (res.status === 401) {
        setErr('Invalid key')
        setBusy(false)
        return
      }
      if (!res.ok) {
        setErr('Error ' + res.status)
        setBusy(false)
        return
      }
      auth.set(k) // only persist on success; layout re-renders into the app
    } catch {
      setErr('Cannot reach the API')
      setBusy(false)
    }
  }

  return (
    <div className="grid min-h-screen place-items-center p-6">
      <Card className="w-full max-w-md">
        <CardHeader>
          <div className="mb-1 flex items-center gap-2">
            <BrandLogo className="text-2xl" />
            <CardTitle>ORCHD · Admin</CardTitle>
          </div>
          <CardDescription>
            Paste the control-plane API key. It is stored only in this browser.
          </CardDescription>
        </CardHeader>
        <CardContent className="flex flex-col gap-3">
          <Input
            type="password"
            placeholder="API key"
            value={val}
            autoFocus
            onChange={(e) => setVal(e.target.value)}
            onKeyDown={(e) => e.key === 'Enter' && connect()}
          />
          {err && <div className="text-sm text-destructive">{err}</div>}
          <Button onClick={connect} disabled={busy}>
            {busy ? 'Connecting…' : 'Connect'}
          </Button>
        </CardContent>
      </Card>
    </div>
  )
}
