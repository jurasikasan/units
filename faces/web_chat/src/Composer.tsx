import { createContext, useContext, useState } from 'react'
import { SquareIcon } from 'lucide-react'
import { Button } from '@/upstream/components/ui/button'
import { Call } from '@/Call'
import type { Writer } from '@/world'

// What the composer's row shows: whether a call can be made here (the
// realtime endpoint answers), who is writing a reply in this chat, and
// how to stop a brain of the person's writing there.
export const ComposerContext = createContext<{ call: boolean; writers: Writer[]; stoppable: (sender: string) => boolean; stop: (sender: string) => Promise<void> }>({
  call: false,
  writers: [],
  stoppable: () => false,
  stop: async () => {},
})

// ComposerRow is Thread's ComposerExtra slot (upstream's thread has none
// of its own), in the composer's own row: a call's buttons where there is
// one, who is writing while they write, and Stop beside a brain that
// holds one of the person's avatars: an action on that brain, through
// reins; what it wrote so far is said, when there was any. Which model
// thinks for an avatar is not picked here: it is the brain holding its
// reins (handed in the row above the chat).
export function ComposerRow() {
  const { call, writers, stoppable, stop } = useContext(ComposerContext)
  const [refused, setRefused] = useState('')
  return (
    <>
      {call && <Call />}
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
