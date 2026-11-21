// NOTICE is the window event the page says a failure with, its detail the
// words for the person: the mic, read-aloud, a call, a chat renamed or
// deleted. src/Notice.tsx shows it; assistant-ui has no place for this.
export const NOTICE = 'units-notice'

export function notify(text: string) {
  window.dispatchEvent(new CustomEvent(NOTICE, { detail: text }))
}
