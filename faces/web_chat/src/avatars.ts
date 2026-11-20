import { useCallback, useEffect, useState } from 'react'
import type { SoundPick } from '@/Models'

const MINE = '/api/reins/me/subjects'
const ONE = (id: string) => `/api/reins/subjects/${encodeURIComponent(id)}`

// The preference keys the face reads, as reins' CONTRACT.md names them:
// on an assistant its chat model, its mouth and the mouth's voice; on a
// person its ear. Each is a model's id at /api/v1/models (a voice: one of
// that speech model's voices); one left out is the agent's own.
export type Preferences = { chat_model?: string; mouth?: string; voice?: string; ear?: string; [other: string]: unknown }

// An avatar: a subject the principal steers, as reins answers it. Its
// subject_type is not read: what an avatar is for here is the classic
// chat's wiring below.
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

// The classic chat: <login> speaks and <login>-assistant answers. Which
// avatar's preference feeds which request is the face's wiring until the
// conversation is kept somewhere that reads them itself. The pair is found
// by nick: the logged-in login's when known, else the first avatar whose
// assistant is also steered.
export const ASSISTANT_SUFFIX = '-assistant'

export type Classic = { person?: Avatar; assistant?: Avatar }

export function classic(avatars: Avatar[] | undefined, login: string | undefined): Classic {
  if (!avatars) return {}
  const byNick = new Map(avatars.map((a) => [a.nick, a]))
  const person =
    (login ? byNick.get(login) : undefined) ?? avatars.find((a) => byNick.has(a.nick + ASSISTANT_SUFFIX))
  return { person, assistant: person && byNick.get(person.nick + ASSISTANT_SUFFIX) }
}

// picks is what a run, the mic, read-aloud and a call are asked with: the
// person's ear, the assistant's chat model, mouth and voice. Nothing set
// is the agent's own.
export function picks({ person, assistant }: Classic): SoundPick {
  const p = assistant?.preferences ?? {}
  return {
    chat: p.chat_model,
    tts: p.mouth,
    voice: p.voice,
    stt: person?.preferences?.ear,
  }
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
