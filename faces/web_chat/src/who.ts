import { useCallback, useState } from 'react'

// Who the person is on this device: nobody (the ghost, '') or one of the
// avatars they steer (its subject id). Kept per device in localStorage, as
// the device id is (src/device.ts): every tab of this browser is the same
// one. Not in the address: a link is shared, a session is not.
const KEY = 'units.who'

function kept() {
  try {
    return localStorage.getItem(KEY) ?? ''
  } catch {
    return ''
  }
}

export function useWho() {
  const [who, setWho] = useState(kept)
  const choose = useCallback((id: string) => {
    setWho(id)
    try {
      localStorage.setItem(KEY, id)
    } catch {
      // kept for as long as the page is open
    }
  }, [])
  return [who, choose] as const
}
