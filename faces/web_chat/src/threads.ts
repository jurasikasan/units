import { useEffect, useMemo, useRef, useState } from 'react'
import type { HttpAgent } from '@ag-ui/client'
import type { AssistantRuntime, ThreadMessage } from '@assistant-ui/react'
import type { UseAgUiThreadListAdapter } from '@assistant-ui/react-ag-ui'

// The page's chats, in its memory: a new one, the list, switching between
// them, renaming, archiving, deleting. Gone on reload: nothing stores them
// yet (threads will). Each is an AG-UI thread: its id is the threadId of
// its runs, and its messages are handed back to the runtime on a switch.

type Chat = { id: string; title?: string; archived?: boolean }

const newID = () => crypto.randomUUID()

// title is what a chat is called until it is renamed: the start of its
// first words.
function title(messages: readonly ThreadMessage[]) {
  for (const m of messages) {
    if (m.role !== 'user') continue
    for (const p of m.content) {
      if (p.type === 'text' && p.text.trim()) {
        const t = p.text.trim().replace(/\s+/g, ' ')
        return t.length > 40 ? t.slice(0, 39) + '…' : t
      }
    }
  }
  return undefined
}

export function useThreads(agent: HttpAgent) {
  const [chats, setChats] = useState<Chat[]>(() => [{ id: agent.threadId }])
  const [current, setCurrent] = useState(agent.threadId)
  const saved = useRef(new Map<string, readonly ThreadMessage[]>())
  const runtime = useRef<AssistantRuntime | null>(null)
  const named = useRef(new Set<string>()) // renamed by the person: kept

  // keep is the current chat's messages, put away before another is shown.
  const keep = () => {
    const r = runtime.current
    if (r) saved.current.set(agent.threadId, r.thread.getState().messages)
  }
  const show = (id: string) => {
    agent.threadId = id
    setCurrent(id)
  }

  const adapter = useMemo<UseAgUiThreadListAdapter>(
    () => ({
      threadId: current,
      threads: chats.filter((c) => !c.archived).map((c) => ({ status: 'regular', id: c.id, title: c.title })),
      archivedThreads: chats.filter((c) => c.archived).map((c) => ({ status: 'archived', id: c.id, title: c.title })),
      onSwitchToNewThread: () => {
        keep()
        const id = newID()
        setChats((cs) => [{ id }, ...cs])
        show(id)
      },
      onSwitchToThread: (id) => {
        keep()
        show(id)
        return { messages: saved.current.get(id) ?? [] }
      },
      onRename: (id, t) => {
        named.current.add(id)
        setChats((cs) => cs.map((c) => (c.id === id ? { ...c, title: t } : c)))
      },
      onArchive: (id) => setChats((cs) => cs.map((c) => (c.id === id ? { ...c, archived: true } : c))),
      onUnarchive: (id) => setChats((cs) => cs.map((c) => (c.id === id ? { ...c, archived: false } : c))),
      onDelete: (id) => {
        saved.current.delete(id)
        setChats((cs) => cs.filter((c) => c.id !== id))
      },
    }),
    // keep and show read refs and the agent only.
    // eslint-disable-next-line react-hooks/exhaustive-deps
    [chats, current, agent],
  )

  // bind gives the hook the runtime built from its adapter, and names the
  // current chat after its first words as they come.
  const [bound, bind] = useState<AssistantRuntime | null>(null)
  useEffect(() => {
    runtime.current = bound
    if (!bound) return
    return bound.thread.subscribe(() => {
      const id = agent.threadId
      if (named.current.has(id)) return
      const t = title(bound.thread.getState().messages)
      setChats((cs) => (cs.some((c) => c.id === id && c.title !== t) ? cs.map((c) => (c.id === id ? { ...c, title: t } : c)) : cs))
    })
  }, [bound, agent])

  return { adapter, bind }
}
