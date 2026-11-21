import { createContext, useContext, useState } from 'react'
import { SquareIcon } from 'lucide-react'
import { Button } from '@/upstream/components/ui/button'
import { Call } from '@/Call'
import type { Writer } from '@/world'

// What the composer's row shows: whether a call can be made here (the
// realtime endpoint answers), who is writing a reply in this chat, and
// how to stop a brain of the person's writing there. `watching` is what
// stands in the composer's place when there is nobody to write as: the
// ghost, or an avatar held elsewhere.
export const ComposerContext = createContext<{
  call: boolean
  writers: Writer[]
  stoppable: (sender: string) => boolean
  stop: (sender: string) => Promise<void>
  watching?: string
}>({
  call: false,
  writers: [],
  stoppable: () => false,
  stop: async () => {},
})

// ComposerRow is Thread's ComposerExtra slot (upstream's thread has none
// of its own), in the composer's own row: a call's buttons where there is
// one, and who is writing with Stop beside a brain of the person's.
export function ComposerRow() {
  const { call } = useContext(ComposerContext)
  return (
    <>
      {call && <Call />}
      <Writing />
    </>
  )
}

// Watching is Thread's ComposerInstead slot (patches/05): with nobody to
// write as, in the composer's place and in the page's flow, who holds the
// avatar and how to step in; who is writing, and Stop beside a brain of the
// person's (never for the ghost, who can do nothing).
export function Watching() {
  const { watching } = useContext(ComposerContext)
  return (
    <div role="status" aria-label="Watching" className="text-muted-foreground flex min-h-10 flex-wrap items-center gap-2 rounded-(--composer-radius) border border-dashed px-3 py-2 text-sm">
      <span className="min-w-0">{watching}</span>
      <Writing />
    </div>
  )
}

function Writing() {
  const { writers, stoppable, stop } = useContext(ComposerContext)
  const [refused, setRefused] = useState('')
  return (
    <>
      {writers.length > 0 && (
        <span role="status" aria-label="Writing" className="text-muted-foreground animate-pulse truncate px-1 text-xs">
          {writers.map((w) => w.name).join(', ')} {writers.length > 1 ? 'are' : 'is'} writing…
        </span>
      )}
      {writers
        .filter((w) => stoppable(w.sender))
        .map((w) => (
          <Button
            key={w.sender}
            type="button"
            variant="outline"
            size="xs"
            aria-label={`Stop ${w.name}`}
            onClick={() => {
              setRefused('')
              stop(w.sender).catch((e) => setRefused(e instanceof Error ? e.message : String(e)))
            }}
          >
            <SquareIcon className="fill-current" /> Stop
          </Button>
        ))}
      {refused && (
        <span role="alert" className="text-destructive truncate px-1 text-xs">
          {refused}
        </span>
      )}
    </>
  )
}
