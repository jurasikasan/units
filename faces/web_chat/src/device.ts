// The device this page is: one id per browser, kept in localStorage, sent
// to reins with every request (X-Device-Id) and on the stream (?device=).
// A principal holds an avatar's reins from a device; its hold lapses when
// the device's stream has been gone a while (reins' CONTRACT.md, section
// 5). Where storage is closed (a private window), the id lives as long as
// the page.
const KEY = 'units.device'

function made() {
  try {
    const kept = localStorage.getItem(KEY)
    if (kept) return kept
    const id = crypto.randomUUID()
    localStorage.setItem(KEY, id)
    return id
  } catch {
    return crypto.randomUUID()
  }
}

export const DEVICE = made()

// withDevice is a fetch that names this device to reins.
export function withDevice(fetcher: typeof fetch): typeof fetch {
  return (input, init) => {
    const url = typeof input === 'string' ? input : input instanceof URL ? input.pathname : input.url
    if (!url.startsWith('/api/reins/')) return fetcher(input, init)
    const headers = new Headers(init?.headers)
    headers.set('X-Device-Id', DEVICE)
    return fetcher(input, { ...init, headers })
  }
}
