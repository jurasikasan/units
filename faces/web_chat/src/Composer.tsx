import { createContext, useContext } from 'react'
import { Call } from '@/Call'

// What the composer's row shows: whether a call can be made here (the
// realtime endpoint answers), and who is writing a reply in this chat.
export const ComposerContext = createContext<{ call: boolean; writers: string[] }>({ call: false, writers: [] })

// ComposerRow is Thread's ComposerExtra slot (upstream's thread has none
// of its own): a call's buttons where there is one, and who is writing,
// while they write. Which model thinks for the other avatar is not picked
// here: it is the brain holding its reins (a driver, drivers/model), not
// the page's.
export function ComposerRow() {
  const { call, writers } = useContext(ComposerContext)
  return (
    <>
      {call && <Call />}
      {writers.length > 0 && (
        <span role="status" aria-label="Writing" className="text-muted-foreground animate-pulse truncate px-1 text-xs">
          {writers.join(', ')} {writers.length > 1 ? 'are' : 'is'} writing…
        </span>
      )}
    </>
  )
}
