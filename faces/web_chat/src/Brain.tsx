import { createContext, useContext } from 'react'
import { Pick } from '@/Models'
import { Call } from '@/Call'

// Which model thinks for the run: not a property of any avatar's body,
// but the brain holding its reins now. Until a driver holds them (UNIT-60)
// the page picks it here, in the composer, for each run and call; it lasts
// while the page is open, the agent's own after a reload.
export type BrainPick = { ids: string[]; value?: string; set: (id: string) => void; call: boolean }

export const BrainContext = createContext<BrainPick>({ ids: [], set: () => {}, call: false })

// ComposerRow is Thread's ComposerExtra slot (upstream's thread has none
// of its own): the brain's picker, and a call's buttons where there is one.
export function ComposerRow() {
  const { ids, value, set, call } = useContext(BrainContext)
  return (
    <>
      <Pick label="Chat model" ids={ids} value={value} onChange={set} variant="ghost" />
      {call && <Call />}
    </>
  )
}
