import { createContext, useContext } from 'react'
import { Call } from '@/Call'

// Whether a call can be made here: the realtime endpoint answers.
export const CallContext = createContext(false)

// ComposerRow is Thread's ComposerExtra slot (upstream's thread has none
// of its own): a call's buttons where there is one. Which model thinks for
// the other avatar is not picked here: it is the brain holding its reins
// (a driver, drivers/model), not the page's.
export function ComposerRow() {
  return useContext(CallContext) ? <Call /> : null
}
