import { NOTICE } from '@/notice'
import type { DictationAdapter, SpeechSynthesisAdapter } from '@assistant-ui/react'

// Sound, turn by turn, through OpenAI's audio API at /api/v1/audio: what
// is said into the mic becomes text in the composer, and a message is read
// aloud. assistant-ui's own adapters for these are the browser's Web Speech,
// which sends the sound wherever the browser's maker chose; these send it
// to whatever answers OpenAI's audio API on the same origin, which picks
// the model and the voice. The face decides nothing about either.

type Listener<T> = (value: T) => void

// VOICE_FAILED is the window event a failure is said with, its detail the
// words for the person; the page shows it (src/notice.ts, src/Notice.tsx).
export const VOICE_FAILED = NOTICE

function said(what: string, error: unknown) {
  console.error(what, error)
  const why = error instanceof Error ? error.message : String(error ?? '')
  window.dispatchEvent(new CustomEvent(VOICE_FAILED, { detail: why ? `${what}: ${why}` : what }))
}

function listeners<T>() {
  const set = new Set<Listener<T>>()
  return {
    add(fn: Listener<T>) {
      set.add(fn)
      return () => {
        set.delete(fn)
      }
    },
    emit(value: T) {
      for (const fn of [...set]) fn(value)
    },
  }
}

// failed reads an OpenAI-shaped error, or says what came back instead.
async function failed(resp: Response): Promise<Error> {
  const text = await resp.text()
  try {
    const message = JSON.parse(text)?.error?.message
    if (message) return new Error(`${resp.status}: ${message}`)
  } catch {
    // not JSON: said below as it is
  }
  return new Error(`${resp.status}: ${text.slice(0, 200) || resp.statusText}`)
}

// A 401 means the door in front no longer knows who this is.
async function post(path: string, init: RequestInit): Promise<Response> {
  const resp = await fetch(path, { method: 'POST', ...init })
  if (resp.status === 401) window.location.reload()
  if (!resp.ok) throw await failed(resp)
  return resp
}

// The browser records in the first of these it can; Opus in WebM is what
// Chrome and Firefox make, MP4 what Safari does. OpenAI's API takes them all.
const RECORDED = ['audio/webm;codecs=opus', 'audio/webm', 'audio/ogg;codecs=opus', 'audio/mp4']

function extension(type: string) {
  if (type.includes('ogg')) return 'ogg'
  if (type.includes('mp4')) return 'm4a'
  return 'webm'
}

// Dictation: the mic is recorded until the person stops it, then the
// recording is transcribed whole and the text lands in the composer. No
// words while speaking: OpenAI's transcriptions answer once, for the whole.
export class TranscriptionDictationAdapter implements DictationAdapter {
  disableInputDuringDictation = true

  private path: string
  private pick: () => { model?: string }

  // pick is the model to ask, read at each call; none: the agent's own.
  constructor(path = '/api/v1/audio/transcriptions', pick: () => { model?: string } = () => ({})) {
    this.path = path
    this.pick = pick
  }

  static isSupported() {
    return typeof MediaRecorder !== 'undefined' && !!navigator.mediaDevices?.getUserMedia
  }

  listen(): DictationAdapter.Session {
    const speechStart = listeners<void>()
    const speech = listeners<DictationAdapter.Result>()
    const speechEnd = listeners<DictationAdapter.Result>()
    let recorder: MediaRecorder | undefined
    let stream: MediaStream | undefined
    let cancelled = false
    const chunks: Blob[] = []
    const path = this.path
    const pick = this.pick

    const session: DictationAdapter.Session = {
      status: { type: 'starting' },
      onSpeechStart: (cb) => speechStart.add(cb),
      onSpeech: (cb) => speech.add(cb),
      onSpeechEnd: (cb) => speechEnd.add(cb),
      cancel() {
        cancelled = true
        if (recorder?.state === 'recording') recorder.stop()
        stream?.getTracks().forEach((t) => t.stop())
        session.status = { type: 'ended', reason: 'cancelled' }
      },
      async stop() {
        if (session.status.type === 'ended') return
        await opened
        if (!recorder || cancelled) return
        const recorded = new Promise<void>((resolve) => recorder!.addEventListener('stop', () => resolve(), { once: true }))
        if (recorder.state === 'recording') recorder.stop()
        await recorded
        stream?.getTracks().forEach((t) => t.stop())
        const type = recorder.mimeType || 'audio/webm'
        const form = new FormData()
        // The model before the sound, where OpenAI's clients put it.
        const { model } = pick()
        if (model) form.append('model', model)
        form.append('file', new Blob(chunks, { type }), `dictation.${extension(type)}`)
        let result: DictationAdapter.Result = { transcript: '', isFinal: true }
        try {
          const resp = await post(path, { body: form })
          const { text } = await resp.json()
          result = { transcript: String(text ?? '').trim(), isFinal: true }
          if (!cancelled && result.transcript) speech.emit(result)
          session.status = { type: 'ended', reason: 'stopped' }
        } catch (error) {
          said('The recording was not transcribed', error)
          session.status = { type: 'ended', reason: 'error' }
        }
        speechEnd.emit(result)
      },
    }

    const opened = (async () => {
      try {
        stream = await navigator.mediaDevices.getUserMedia({ audio: true })
        if (cancelled) {
          stream.getTracks().forEach((t) => t.stop())
          return
        }
        const mimeType = RECORDED.find((t) => MediaRecorder.isTypeSupported(t))
        recorder = new MediaRecorder(stream, mimeType ? { mimeType } : undefined)
        recorder.addEventListener('dataavailable', (e) => {
          if (e.data.size > 0) chunks.push(e.data)
        })
        recorder.start()
        session.status = { type: 'running' }
        speechStart.emit()
      } catch (error) {
        said('No microphone', error)
        session.status = { type: 'ended', reason: 'error' }
        speechEnd.emit({ transcript: '', isFinal: true })
      }
    })()

    return session
  }
}

// Read aloud: a message's words are spoken whole by the speech endpoint,
// and played when they have all come.
export class SpeechEndpointAdapter implements SpeechSynthesisAdapter {
  private path: string
  private pick: () => { model?: string; voice?: string }

  // pick is the model and voice to ask, read at each call; none: the
  // agent's own.
  constructor(path = '/api/v1/audio/speech', pick: () => { model?: string; voice?: string } = () => ({})) {
    this.path = path
    this.pick = pick
  }

  speak(text: string): SpeechSynthesisAdapter.Utterance {
    const changed = listeners<void>()
    const abort = new AbortController()
    let audio: HTMLAudioElement | undefined
    let url: string | undefined

    const utterance: SpeechSynthesisAdapter.Utterance = {
      status: { type: 'starting' },
      subscribe: (cb) => changed.add(cb),
      cancel() {
        abort.abort()
        audio?.pause()
        end({ type: 'ended', reason: 'cancelled' })
      },
    }

    const end = (status: SpeechSynthesisAdapter.Status) => {
      if (utterance.status.type === 'ended') return
      utterance.status = status
      if (url) URL.revokeObjectURL(url)
      changed.emit()
    }

    ;(async () => {
      try {
        const resp = await post(this.path, {
          headers: { 'Content-Type': 'application/json' },
          body: JSON.stringify({ input: text, ...this.pick() }),
          signal: abort.signal,
        })
        url = URL.createObjectURL(await resp.blob())
        if (utterance.status.type === 'ended') return
        audio = new Audio(url)
        audio.addEventListener('ended', () => end({ type: 'ended', reason: 'finished' }))
        audio.addEventListener('error', () => end({ type: 'ended', reason: 'error', error: audio?.error }))
        await audio.play()
        utterance.status = { type: 'running' }
        changed.emit()
      } catch (error) {
        if (abort.signal.aborted) return
        said('Nothing to play', error)
        end({ type: 'ended', reason: 'error', error })
      }
    })()

    return utterance
  }
}
