import { useEffect, useState } from 'react'
import { VOICE_FAILED } from '@/voice'

// VoiceNotice says, for a few seconds, why the mic or read-aloud did
// nothing: the endpoint's own words. assistant-ui has no place for this.
export function VoiceNotice() {
  const [text, setText] = useState('')
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined
    const show = (e: Event) => {
      setText(String((e as CustomEvent).detail ?? ''))
      clearTimeout(timer)
      timer = setTimeout(() => setText(''), 8000)
    }
    window.addEventListener(VOICE_FAILED, show)
    return () => {
      window.removeEventListener(VOICE_FAILED, show)
      clearTimeout(timer)
    }
  }, [])
  if (!text) return null
  return (
    <div
      role="alert"
      onClick={() => setText('')}
      className="bg-destructive/10 text-destructive border-destructive/30 fixed inset-x-4 top-4 z-50 mx-auto max-w-xl cursor-pointer rounded-xl border px-4 py-2 text-sm wrap-break-word"
    >
      {text.length > 300 ? text.slice(0, 300) + '…' : text}
    </div>
  )
}
