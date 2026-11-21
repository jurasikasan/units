import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
import type { AppendMessage, CompleteAttachment, ThreadMessage, ThreadMessageLike } from '@assistant-ui/react'
import type { Avatar, Brain } from '@/avatars'
import { DEVICE } from '@/device'
import { notify } from '@/notice'

// The world as reins shows it to the person (reins' CONTRACT.md): the
// threads their avatars are in, each thread's entries as `threads` keeps
// them, and one stream that brings each new entry, and the pieces of a
// reply still being written, as they come. The page keeps none of it: a
// reload reads it all again.

const THREADS = '/api/reins/me/threads'
const ENTRIES = (id: string) => `/api/reins/threads/${encodeURIComponent(id)}/entries`
// The stream names this device: while it is open, the device is there,
// and the reins it holds do not lapse.
const STREAM = `/api/reins/me/stream?device=${encodeURIComponent(DEVICE)}`
const STOP = (id: string) => `/api/reins/threads/${encodeURIComponent(id)}/stop`
const THREAD = (id: string) => `/api/reins/threads/${encodeURIComponent(id)}`

export type Thread = { id: string; name: string; members: string[]; created_at: string }

// A block of an entry's content, threads' own (its CONTRACT.md, section 1).
export type Block =
  | { type: 'text'; text: string }
  | { type: 'image'; url: string; media_type: string; sha256: string }
  | { type: 'file'; url: string; media_type: string; sha256: string; name?: string }
  | { type: 'audio'; url: string; media_type: string; sha256: string; transcript?: string }
  | { type: string; [field: string]: unknown }

export type Entry = { id: string; thread: string; seq: number; sender: string; kind: string; at: string; content: Block[]; name?: string }

// A reply being written: never journaled, gone when its entry comes (or
// it finished or was stopped), or on reload. `failed` says why it ended
// with nothing written.
type Writing = { reply: string; sender: string; text: string; reasoning: string; failed?: string }

// A piece of a reply, as reins relays it (`writing`): the driver's event's
// data with its type beside (drivers/model's CONTRACT.md).
type Piece = { type: string; thread: string; sender: string; reply: string; text?: string; part?: string; message?: string }

const sorted = (es: Entry[]) => [...es].sort((a, b) => a.seq - b.seq)

// merge adds entries to a thread's, each seq once.
function merge(have: Entry[] | undefined, more: Entry[]) {
  const bySeq = new Map((have ?? []).map((e) => [e.seq, e]))
  for (const e of more) bySeq.set(e.seq, e)
  return sorted([...bySeq.values()])
}

// title is what a chat is called: its name, or the start of the first words
// said in it.
function title(t: Thread, entries: Entry[] | undefined) {
  if (t.name) return t.name
  for (const e of entries ?? []) {
    if (e.kind !== 'create') continue
    for (const b of e.content) {
      if (b.type === 'text' && typeof b.text === 'string' && !b.text.startsWith('<attachment') && b.text.trim()) {
        const s = b.text.trim().replace(/\s+/g, ' ')
        return s.length > 40 ? s.slice(0, 39) + '…' : s
      }
    }
  }
  return undefined
}

// The text of a text file goes into the journal as assistant-ui words it
// (`<attachment name="…">…</attachment>`); read back, it is an attachment
// again.
const ATTACHED = /^<attachment name="([^"]*)">\n?([\s\S]*?)\n?<\/attachment>$/

// toMessage is an entry as the page shows it: the person's own when its
// sender is the avatar the person is in on this device, everyone else's on
// the other side, with name and time (for the ghost, every message).
// Pictures, files and attached text on the person's side are attachments,
// as they were when sent.
export function toMessage(e: Entry, me: string | undefined, names: Map<string, string>, shownAs?: string): ThreadMessageLike {
  const mine = e.sender === me
  const content: Exclude<ThreadMessageLike['content'], string>[number][] = []
  const attachments: CompleteAttachment[] = []
  for (const [i, b] of e.content.entries()) {
    const id = `${e.id}-${i}`
    if (b.type === 'text' && typeof b.text === 'string') {
      const m = mine ? ATTACHED.exec(b.text) : null
      if (m) attachments.push({ id, type: 'document', name: m[1]!, contentType: 'text/plain', status: { type: 'complete' }, content: [{ type: 'text', text: b.text }] })
      else content.push({ type: 'text', text: b.text })
    } else if (b.type === 'image' && typeof b.url === 'string') {
      if (mine) attachments.push({ id, type: 'image', name: 'picture', contentType: String(b.media_type), status: { type: 'complete' }, content: [{ type: 'image', image: b.url }] })
      else content.push({ type: 'image', image: b.url })
    } else if (b.type === 'file' && typeof b.url === 'string') {
      const name = typeof b.name === 'string' ? b.name : 'file'
      const part = { type: 'file' as const, data: b.url, mimeType: String(b.media_type), filename: name }
      if (mine) attachments.push({ id, type: 'document', name, contentType: part.mimeType, status: { type: 'complete' }, content: [part] })
      else content.push(part)
    } else if (b.type === 'audio' && typeof b.transcript === 'string') {
      content.push({ type: 'text', text: b.transcript })
    }
  }
  return {
    id: shownAs ?? e.id,
    role: mine ? 'user' : 'assistant',
    content,
    ...(mine ? { attachments } : { status: { type: 'complete', reason: 'stop' } }),
    createdAt: new Date(e.at),
    metadata: { custom: { sender: e.sender, senderName: names.get(e.sender) ?? '', seq: e.seq } },
  }
}

function writingMessage(w: Writing, names: Map<string, string>): ThreadMessageLike {
  const content: Exclude<ThreadMessageLike['content'], string>[number][] = []
  if (w.reasoning) content.push({ type: 'reasoning', text: w.reasoning })
  if (w.text) content.push({ type: 'text', text: w.text })
  return {
    id: `writing-${w.reply}`,
    role: 'assistant',
    content,
    status: w.failed ? { type: 'incomplete', reason: 'error', error: w.failed } : { type: 'running' },
    metadata: { custom: { sender: w.sender, senderName: names.get(w.sender) ?? '', writing: true } },
  }
}

const readDataURL = (url: string) => {
  const comma = url.indexOf(',')
  const head = url.slice(5, comma) // after "data:"
  const media = head.split(';')[0] || 'application/octet-stream'
  const body = url.slice(comma + 1)
  const bytes = head.endsWith(';base64') ? Uint8Array.from(atob(body), (c) => c.charCodeAt(0)) : new TextEncoder().encode(decodeURIComponent(body))
  return { media, bytes }
}

async function sha256(bytes: Uint8Array) {
  const d = await crypto.subtle.digest('SHA-256', bytes as BufferSource)
  return [...new Uint8Array(d)].map((x) => x.toString(16).padStart(2, '0')).join('')
}

// toBlocks is what the person sends, as threads keeps it: words as text, a
// picture as an image block whose url is its data: URL, a text file as the
// text assistant-ui made of it, a PDF as a file block; each picture and file
// with its bytes' sha256. Keeping the bytes elsewhere (artifact_store) is a
// later task: until then they live inline in the journal.
export async function toBlocks(m: AppendMessage): Promise<Block[]> {
  const parts = [...(m.attachments ?? []).flatMap((a) => a.content ?? []), ...m.content]
  const out: Block[] = []
  for (const p of parts) {
    if (p.type === 'text') {
      if (p.text.trim()) out.push({ type: 'text', text: p.text })
    } else if (p.type === 'image') {
      const { media, bytes } = readDataURL(p.image)
      out.push({ type: 'image', url: p.image, media_type: media, sha256: await sha256(bytes) })
    } else if (p.type === 'file') {
      const url = p.data.startsWith('data:') ? p.data : `data:${p.mimeType};base64,${p.data}`
      const { bytes } = readDataURL(url)
      out.push({ type: 'file', url, media_type: p.mimeType, sha256: await sha256(bytes), ...(p.filename ? { name: p.filename } : {}) })
    }
  }
  return out
}

// What the stream says besides the world: who holds an avatar now
// (`hold`), and that it is open again (`ready`: what was missed is read
// again).
export type Heard = { hold?: (subject: string, brain: Brain) => void; ready?: () => void }

// A brain writing in the chat shown: who, by name.
export type Writer = { sender: string; name: string }

// The thread the address names: /t/<thread id>.
const named = () => /^\/t\/([^/]+)$/.exec(location.pathname)?.[1]
const address = (id: string | null) => (id ? `/t/${encodeURIComponent(id)}` : '/')

// useWorld is the page's view of the world through reins, as `me`, the
// avatar the person is in on this device (none: the ghost, who sees every
// thread of the principal's avatars). In an avatar it sees the threads it
// is a member of (its perception, for now), and writes as it when `writes`
// (its reins held here), opening a new chat with `other`. current is the
// chat shown; null is a new one, not opened until something is said in it.
export function useWorld(fetcher: typeof fetch, me: Avatar | undefined, writes: boolean, other: Avatar | undefined, avatars: Avatar[] | undefined, listen: Heard = {}) {
  const listenRef = useRef(listen)
  useEffect(() => {
    listenRef.current = listen
  })
  const [threads, setThreads] = useState<Thread[]>()
  const [entries, setEntries] = useState<Record<string, Entry[]>>({})
  const [writing, setWriting] = useState<Record<string, Writing[]>>({})
  // The chat picked: a thread's id, null for a new one, undefined for none
  // picked yet. What is shown is worked out from it below.
  const [chosen, setCurrent] = useState<string | null>()
  // What was said in a call, by chat: shown for as long as the page is
  // open, never journaled (a call's transcripts go to threads with UNIT-52).
  const [spoken, setSpoken] = useState<Record<string, ThreadMessage[]>>({})
  const [draft, setDraft] = useState(() => crypto.randomUUID())
  const loaded = useRef(new Set<string>())
  // A reply written here keeps, once said, the id it was written under: to
  // the runtime it is the same message grown whole, not another one beside
  // it (a branch). Entry id → that id, for as long as the page is open.
  const shownAs = useRef(new Map<string, string>())
  const entriesRef = useRef(entries)
  const threadsRef = useRef(threads)
  const writingRef = useRef(writing)
  useEffect(() => {
    entriesRef.current = entries
    threadsRef.current = threads
    writingRef.current = writing
  })

  // The list is read again whenever the stream brings a thread not listed,
  // so reads overlap; one asked earlier may answer later. Only an answer to
  // a read asked after the one shown is kept: a list from before a thread
  // was opened never hides it again.
  const asked = useRef(0)
  const shown = useRef(0)
  const loadThreads = useCallback(async () => {
    const n = ++asked.current
    const r = await fetcher(THREADS)
    if (!r.ok) return
    const list: Thread[] = await r.json()
    if (n < shown.current) return
    shown.current = n
    setThreads(list)
    // A few words of each, to call it by.
    for (const t of list.slice(0, 30)) {
      if (t.name || entriesRef.current[t.id]?.length) continue
      fetcher(`${ENTRIES(t.id)}?limit=5`)
        .then((r) => (r.ok ? r.json() : []))
        .then((es: Entry[]) => setEntries((all) => ({ ...all, [t.id]: merge(all[t.id], es) })))
        .catch(() => {})
    }
  }, [fetcher])

  // loadEntries reads a thread from after the last seq the page has, page
  // by page, to its end.
  const loadEntries = useCallback(
    async (id: string) => {
      let after = loaded.current.has(id) ? (entriesRef.current[id]?.at(-1)?.seq ?? 0) : 0
      for (;;) {
        const r = await fetcher(`${ENTRIES(id)}?after=${after}&limit=1000`)
        if (!r.ok) return
        const page: Entry[] = await r.json()
        setEntries((all) => ({ ...all, [id]: merge(all[id], page) }))
        if (page.length < 1000) break
        after = page.at(-1)!.seq
      }
      loaded.current.add(id)
    },
    [fetcher],
  )

  // The stream, opened before anything is read: what comes while the
  // threads are read is not missed, and an entry had twice is one seq.
  // EventSource comes back by itself with Last-Event-ID; a stream that
  // cannot (the door said 401, reins is down) is opened again from the
  // last id after a while, and a 401 reloads the page through fetcher.
  useEffect(() => {
    let es: EventSource | undefined
    let last = ''
    let timer: ReturnType<typeof setTimeout> | undefined
    let live = true
    const reloadAll = () => {
      loaded.current.clear()
      setEntries({})
      setWriting({})
      void loadThreads()
    }
    const open = () => {
      es = new EventSource(last ? `${STREAM}?since=${encodeURIComponent(last)}` : STREAM)
      es.addEventListener('ready', (ev) => {
        last = (ev as MessageEvent).lastEventId || last
        void loadThreads()
        for (const id of loaded.current) void loadEntries(id)
        listenRef.current.ready?.()
      })
      es.addEventListener('hold', (ev) => {
        const { subject, ...brain } = JSON.parse((ev as MessageEvent).data) as Brain & { subject: string }
        listenRef.current.hold?.(subject, brain)
      })
      es.addEventListener('reset', reloadAll)
      es.addEventListener('entry', (ev) => {
        const me = ev as MessageEvent
        last = me.lastEventId || last
        const e: Entry = JSON.parse(me.data)
        setEntries((all) => ({ ...all, [e.thread]: merge(all[e.thread], [e]) }))
        if (e.kind === 'create') {
          // The finished reply is the entry: the words written under a reply
          // by its sender, said whole. What was being written is gone, and
          // the entry is shown under its id. Another entry by the same
          // sender (a person acting as it) leaves the reply being written.
          const said = e.content.map((b) => (b.type === 'text' ? String(b.text) : '')).join('')
          const w = writingRef.current[e.thread]?.find((x) => x.sender === e.sender && !x.failed && x.text === said)
          if (w) {
            shownAs.current.set(e.id, `writing-${w.reply}`)
            setWriting((all) => ({ ...all, [e.thread]: (all[e.thread] ?? []).filter((x) => x.reply !== w.reply) }))
          }
        }
        // A thread not listed yet, or its members or name changed: the list again.
        if (!threadsRef.current?.some((t) => t.id === e.thread) || e.kind === 'join' || e.kind === 'leave' || e.kind === 'update') void loadThreads()
      })
      es.addEventListener('writing', (ev) => {
        const p: Piece = JSON.parse((ev as MessageEvent).data)
        setWriting((all) => {
          const list = [...(all[p.thread] ?? [])]
          let w = list.find((x) => x.reply === p.reply)
          if (!w) {
            w = { reply: p.reply, sender: p.sender, text: '', reasoning: '' }
            list.push(w)
          }
          const i = list.indexOf(w)
          if (p.type.endsWith('.delta')) list[i] = p.part === 'reasoning' ? { ...w, reasoning: w.reasoning + (p.text ?? '') } : { ...w, text: w.text + (p.text ?? '') }
          else if (p.type.endsWith('.failed')) list[i] = { ...w, failed: p.message || 'the reply failed' }
          else if (p.type.endsWith('.finished') || p.type.endsWith('.stopped')) list.splice(i, 1)
          return { ...all, [p.thread]: list }
        })
      })
      es.onerror = () => {
        if (es?.readyState !== EventSource.CLOSED || !live) return
        es.close()
        timer = setTimeout(() => {
          // Asked once more through fetcher: a 401 there reloads the page.
          fetcher(THREADS).catch(() => {}).finally(() => live && open())
        }, 3000)
      }
    }
    open()
    return () => {
      live = false
      clearTimeout(timer)
      es?.close()
    }
  }, [fetcher, loadThreads, loadEntries])

  // What is seen from here: every thread for the ghost, the threads the
  // avatar is in for an avatar.
  const seen = useMemo(() => (threads === undefined ? undefined : me ? threads.filter((t) => t.members.includes(me.id)) : threads), [threads, me])

  // The chat shown: once the threads are read, the one picked, else the one
  // the address names (a reload stays in it), else the newest seen. One not
  // seen from here (another avatar picked, deleted) gives way to the newest
  // seen, and so does a new chat with nobody to write it.
  const current = useMemo(() => {
    if (!seen) return undefined
    const newest = seen[0]?.id ?? null
    const c = chosen === undefined ? named() : chosen
    if (c === null) return writes ? null : newest
    return c && seen.some((t) => t.id === c) ? c : newest
  }, [seen, chosen, writes])

  // The chat shown is in the address, /t/<thread id>: a reload, or the link
  // sent to oneself, opens it.
  useEffect(() => {
    if (current) void loadEntries(current)
    if (current !== undefined && location.pathname !== address(current)) history.replaceState(null, '', address(current))
  }, [current, loadEntries])

  const names = useMemo(() => new Map((avatars ?? []).map((a) => [a.id, a.name || a.nick])), [avatars])

  const messages = useMemo(() => {
    const key = current ?? draft
    const said = (current ? (entries[current] ?? []) : []).filter((e) => e.kind === 'create').map((e) => toMessage(e, me?.id, names, shownAs.current.get(e.id)))
    const all: ThreadMessageLike[] = [...said, ...((spoken[key] ?? []) as ThreadMessageLike[])]
    const at = (m: ThreadMessageLike) => (m.createdAt ? new Date(m.createdAt).getTime() : 0)
    all.sort((a, b) => at(a) - at(b))
    return [...all, ...(current ? (writing[current] ?? []) : []).map((w) => writingMessage(w, names))]
  }, [current, draft, entries, spoken, writing, me, names])

  // onVoiceTranscript: a call's finished words, kept in the page under the
  // chat they were said in.
  const heard = useCallback(
    (m: ThreadMessage) => {
      const key = current ?? draft
      setSpoken((all) => ({ ...all, [key]: [...(all[key] ?? []).filter((x) => x.id !== m.id), m] }))
    },
    [current, draft],
  )

  // say appends what the person said, as the avatar they are in, held
  // here; a new chat is opened first, with the other avatar in it. With
  // nobody to write as, the page has no composer.
  const say = useCallback(
    async (m: AppendMessage) => {
      if (!me || !writes) throw new Error('nobody to write as: take an avatar on this device')
      const content = await toBlocks(m)
      if (content.length === 0) return
      let id = current
      if (!id) {
        const r = await fetcher(THREADS, {
          method: 'POST',
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ as: me.id, with: other ? [other.id] : [] }),
        })
        if (!r.ok) throw new Error((await r.text()).trim() || `${r.status} ${r.statusText}`)
        const t: Thread = await r.json()
        id = t.id
        loaded.current.add(id)
        setThreads((ts) => [t, ...(ts ?? []).filter((x) => x.id !== t.id)])
      }
      const r = await fetcher(ENTRIES(id), {
        method: 'POST',
        headers: { 'Content-Type': 'application/json' },
        body: JSON.stringify({ as: me.id, content }),
      })
      if (!r.ok) throw new Error((await r.text()).trim() || `${r.status} ${r.statusText}`)
      const e: Entry = await r.json()
      setEntries((all) => ({ ...all, [e.thread]: merge(all[e.thread], [e]) }))
      setCurrent(id)
    },
    [fetcher, me, writes, other, current],
  )

  const list = useMemo(() => (seen ?? []).map((t) => ({ status: 'regular' as const, id: t.id, title: title(t, entries[t.id]) })), [seen, entries])

  // rename names a chat as an avatar held here that is in it, the one the
  // page acts as first: an update entry, seen by everyone in it. The label
  // changes here at once, and elsewhere when the entry comes down their
  // stream. Holding none of its members here, the page only watches, and
  // says so.
  const rename = useCallback(
    async (id: string, name: string) => {
      try {
        const members = threadsRef.current?.find((t) => t.id === id)?.members ?? []
        const here = (avatars ?? []).filter((a) => a.current_brain?.here && members.includes(a.id))
        const as = here.find((a) => a.id === me?.id) ?? here[0]
        if (!as) throw new Error('you only watch this chat: take the reins of an avatar in it to rename it')
        const r = await fetcher(THREAD(id), { method: 'PATCH', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ as: as.id, name }) })
        if (!r.ok) throw new Error((await r.text()).trim() || `${r.status} ${r.statusText}`)
        const e: Entry = await r.json()
        setThreads((ts) => ts?.map((t) => (t.id === id ? { ...t, name: e.name ?? name } : t)))
      } catch (err) {
        notify(`Not renamed: ${(err as Error).message}`)
        throw err
      }
    },
    [fetcher, me, avatars],
  )

  // remove deletes a chat for good, for everyone in it (reins' DELETE): the
  // page forgets it and, when it was the one shown, moves to the newest
  // left, or a new one. Nobody else is told: their pages find it gone.
  const remove = useCallback(
    async (id: string) => {
      const r = await fetcher(THREAD(id), { method: 'DELETE' })
      if (!r.ok && r.status !== 404) {
        const why = (await r.text()).trim() || `${r.status} ${r.statusText}`
        notify(`Not deleted: ${why}`)
        throw new Error(why)
      }
      loaded.current.delete(id)
      const left = (threadsRef.current ?? []).filter((t) => t.id !== id)
      setThreads(left)
      setEntries(({ [id]: _gone, ...rest }) => rest)
      setWriting(({ [id]: _gone, ...rest }) => rest)
      setCurrent((c) => (c === id ? undefined : c))
    },
    [fetcher],
  )

  const threadList = useMemo(
    () => ({
      threadId: current ?? draft,
      isLoading: threads === undefined,
      threads: list,
      onSwitchToNewThread: () => {
        if (!writes) return
        setDraft(crypto.randomUUID())
        setCurrent(null)
      },
      onSwitchToThread: (id: string) => setCurrent(id),
      onRename: rename,
      onDelete: remove,
    }),
    [current, draft, threads, list, rename, remove, writes],
  )

  // Who is writing a reply in the chat shown now, by name: the page says so
  // beside the composer, as Matrix's typing does.
  const writers = useMemo<Writer[]>(() => {
    const out = new Map<string, Writer>()
    for (const w of current ? (writing[current] ?? []) : []) if (!w.failed) out.set(w.sender, { sender: w.sender, name: names.get(w.sender) || 'someone' })
    return [...out.values()]
  }, [current, writing, names])

  // stop asks reins to stop the reply a brain writes as `sender` here: an
  // action on the brain, not on this page's request. What it wrote so far
  // comes back as an entry, when there was any.
  const stop = useCallback(
    async (sender: string) => {
      if (!current) return
      const r = await fetcher(STOP(current), { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify({ subject: sender }) })
      if (!r.ok) throw new Error((await r.text()).trim() || `${r.status} ${r.statusText}`)
    },
    [fetcher, current],
  )

  return { messages, say, heard, threadList, writers, stop, current, ready: threads !== undefined }
}
