import { useCallback, useEffect, useState } from 'react'
import type { SoundPick } from '@/Models'

const MINE = '/api/reins/me/subjects'
const ONE = (id: string) => `/api/reins/subjects/${encodeURIComponent(id)}`

// The preference keys the face reads, as reins' CONTRACT.md names them,
// the same on every avatar: its ear, its mouth and the mouth's voice. Each
// is a model's id at /api/v1/models (a voice: one of that speech model's
// voices); one left out is the agent's own.
export type Preferences = { ear?: string; mouth?: string; voice?: string; [other: string]: unknown }

// An avatar: a subject the principal steers, as reins answers it. Avatars
// are all alike: none is a human or a machine by kind.
export type Avatar = { id: string; nick: string; name: string; bio?: string; preferences?: Preferences }

// A change of one avatar, as reins takes it: a field left out is kept,
// preferences merged key by key, a key set to null taken out.
export type Change = { name?: string; bio?: string; preferences?: Record<string, string | null> }

// useAvatars is the avatars the principal steers, from reins on the same
// origin, and a way to change one; undefined when nothing answers there.
export function useAvatars(fetcher: typeof fetch) {
  const [avatars, setAvatars] = useState<Avatar[]>()
  useEffect(() => {
    let live = true
    fetcher(MINE)
      .then((r) => (r.ok ? r.json() : undefined))
      .then((list?: Avatar[]) => {
        if (live && Array.isArray(list)) setAvatars(list)
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [fetcher])

  // save sends a change and keeps what reins answers: the avatar as it
  // is now stored. A refusal throws, in reins' own words.
  const save = useCallback(
    async (id: string, change: Change) => {
      const resp = await fetcher(ONE(id), {
        method: 'PATCH',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify(change),
      })
      if (!resp.ok) throw new Error((await resp.text()).trim() || `${resp.status} ${resp.statusText}`)
      const saved: Avatar = await resp.json()
      setAvatars((list) => list?.map((a) => (a.id === saved.id ? saved : a)))
      return saved
    },
    [fetcher],
  )
  return { avatars, save }
}

// Every avatar is a body like any other: each has an ear (`ear`) and a
// mouth with its voice (`mouth`, `voice`), whoever is in it. Which model
// thinks for it is not the body's: that is the brain holding its reins
// (a driver, drivers/model).
//
// The page acts as the avatar named by its login (the first, when none
// is): what the person says goes into threads as that avatar, and a new
// chat is opened with the first other one, whose mouth and voice read an
// answer aloud. Nothing in a nick says what an avatar is for.
export type Wiring = { me?: Avatar; other?: Avatar }

export function wiring(avatars: Avatar[] | undefined, login: string | undefined): Wiring {
  if (!avatars || avatars.length === 0) return {}
  const me = avatars.find((a) => a.nick === login) ?? avatars[0]
  return { me, other: avatars.find((a) => a.id !== me.id) }
}

// picks is what the mic, read-aloud and a call are asked with: my ear, the
// other's mouth and voice. Nothing set is the agent's own.
export function picks({ me, other }: Wiring): SoundPick {
  const p = other?.preferences ?? {}
  return { tts: p.mouth, voice: p.voice, stt: me?.preferences?.ear }
}

// initials are what an avatar circle shows: the first letters of up to two
// words of a name, a nick or a login.
export const initials = (s: string) =>
  s
    .split(/[\s._@-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0]!.toUpperCase())
    .join('')
