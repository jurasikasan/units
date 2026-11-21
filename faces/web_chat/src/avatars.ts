import { useCallback, useEffect, useState } from 'react'
import type { SoundPick } from '@/Models'

const MINE = '/api/reins/me/subjects'
const ONE = (id: string) => `/api/reins/subjects/${encodeURIComponent(id)}`
const BRAINS = '/api/reins/brains'

// The preference keys the face reads, as reins' CONTRACT.md names them,
// the same on every avatar: its ear, its mouth and the mouth's voice. Each
// is a model's id at /api/v1/models (a voice: one of that speech model's
// voices); one left out is the agent's own.
export type Preferences = { ear?: string; mouth?: string; voice?: string; [other: string]: unknown }

// Who holds an avatar's reins now, as reins shows it to this principal
// from this device (`current_brain`): a principal (you, here or on
// another device, or another steward), a brain (a driver's profile), or
// nobody. Never the avatar's own: the characters never see it.
export type Brain = { kind: 'principal' | 'brain' | 'offline'; principal?: string; profile?: string; you?: boolean; here?: boolean }

// An avatar: a subject the principal steers, as reins answers it. Avatars
// are all alike: none is a human or a machine by kind.
export type Avatar = { id: string; nick: string; name: string; bio?: string; preferences?: Preferences; current_brain?: Brain }

// holderLabel says who holds an avatar, in a few words: you here, you on
// another device, another steward, a brain (its profile), or nobody.
export function holderLabel(b: Brain | undefined) {
  if (!b || b.kind === 'offline') return 'nobody'
  if (b.kind === 'brain') return `brain ${b.profile ?? ''}`.trim()
  if (b.here) return 'you, here'
  if (b.you) return 'you, on another device'
  return b.principal ?? 'someone'
}

// A change of one avatar, as reins takes it: a field left out is kept,
// preferences merged key by key, a key set to null taken out.
export type Change = { name?: string; bio?: string; preferences?: Record<string, string | null> }

// useAvatars is the avatars the principal steers, from reins on the same
// origin, who holds each, and a way to change one, take its reins, give
// them back or hand them to a brain; undefined when nothing answers there.
export function useAvatars(fetcher: typeof fetch) {
  const [avatars, setAvatars] = useState<Avatar[]>()
  const [profiles, setProfiles] = useState<string[]>([])
  // reload reads them again: the stream was away, and who holds what may
  // have changed meanwhile.
  const reload = useCallback(
    () =>
      fetcher(MINE)
        .then((r) => (r.ok ? r.json() : undefined))
        .then((list: unknown) => {
          if (Array.isArray(list)) setAvatars(list as Avatar[])
        })
        .catch(() => {}),
    [fetcher],
  )
  useEffect(() => {
    fetcher(MINE)
      .then((r) => (r.ok ? r.json() : undefined))
      .then((list: unknown) => {
        if (Array.isArray(list)) setAvatars(list as Avatar[])
      })
      .catch(() => {})
    fetcher(BRAINS)
      .then((r) => (r.ok ? r.json() : []))
      .then((ps: unknown) => setProfiles(Array.isArray(ps) ? ps.map(String) : []))
      .catch(() => {})
  }, [fetcher])

  // held keeps who holds an avatar now: from a take, a hand, or the stream.
  const held = useCallback((id: string, brain: Brain) => {
    setAvatars((list) => list?.map((a) => (a.id === id ? { ...a, current_brain: brain } : a)))
  }, [])

  // act asks reins to move an avatar's reins; a refusal throws, in its words.
  const act = useCallback(
    async (id: string, what: 'take' | 'give-back' | 'hand', profile?: string) => {
      const resp = await fetcher(`${ONE(id)}/${what}`, {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: what === 'hand' ? JSON.stringify({ profile }) : undefined,
      })
      if (!resp.ok) throw new Error((await resp.text()).trim() || `${resp.status} ${resp.statusText}`)
      held(id, what === 'give-back' ? { kind: 'offline' } : await resp.json())
    },
    [fetcher, held],
  )
  const take = useCallback((id: string) => act(id, 'take'), [act])
  const giveBack = useCallback((id: string) => act(id, 'give-back'), [act])
  const hand = useCallback((id: string, profile: string) => act(id, 'hand', profile), [act])

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
  return { avatars, save, reload, held, take, giveBack, hand, profiles }
}

// Every avatar is a body like any other: each has an ear (`ear`) and a
// mouth with its voice (`mouth`, `voice`), whoever is in it. Which model
// thinks for it is not the body's: that is the brain holding its reins
// (a driver, drivers/model).
//
// The page is who the person is on this device (src/who.ts): nobody (the
// ghost) or an avatar picked. In an avatar, its own messages are on the
// person's side, and it writes as it only while its reins are held here. A
// new chat is opened with the first other avatar, whose mouth and voice
// read an answer aloud. Nothing in a nick says what an avatar is for.
export type Wiring = { me?: Avatar; other?: Avatar; writes: boolean }

export function wiring(avatars: Avatar[] | undefined, who: string): Wiring {
  const me = who ? avatars?.find((a) => a.id === who) : undefined
  if (!me) return { writes: false }
  return { me, other: avatars?.find((a) => a.id !== me.id), writes: !!me.current_brain?.here }
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
