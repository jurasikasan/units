import { useEffect, useState } from 'react'
import { NOTICE } from '@/notice'

// Notice says, for a few seconds, why something the person asked for did
// nothing (src/notice.ts): the mic, read-aloud, a call, a chat renamed or
// deleted, in the words of whoever refused.
export function Notice() {
  const [text, setText] = useState('')
  useEffect(() => {
    let timer: ReturnType<typeof setTimeout> | undefined
    const show = (e: Event) => {
      setText(String((e as CustomEvent).detail ?? ''))
      clearTimeout(timer)
      timer = setTimeout(() => setText(''), 8000)
    }
    window.addEventListener(NOTICE, show)
    return () => {
      window.removeEventListener(NOTICE, show)
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
