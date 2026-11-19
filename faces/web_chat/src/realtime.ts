import { createVoiceSession, type RealtimeVoiceAdapter } from '@assistant-ui/react'
import { VOICE_FAILED } from '@/voice'

// A call: sound both ways, live, in OpenAI's Realtime schema over a
// WebSocket to whatever answers /api/v1/realtime on the same origin (the
// mind, which picks the model and holds its key). assistant-ui's own
// realtime adapters go straight to a provider with a key in the page, so
// this one is ours. The mic goes out as PCM16 at 24 kHz, the schema's
// default; the answer's sound is played as it comes; what each side said
// lands in the thread as the runtime's voice messages.
//
// An interruption is taken from either of two signs, since a real model
// was seen to send only the second (contracts/edges.md): the schema's
// input_audio_buffer.speech_started while an answer plays, or the answer's
// response.done before its output_audio.done. Either stops what is playing
// and tells the model how much of the answer was heard (truncate).

const RATE = 24000
const CHUNK = RATE / 10 // 100 ms of the mic per message

function said(what: string, error?: unknown) {
  console.error(what, error)
  const why = error instanceof Error ? error.message : String(error ?? '')
  window.dispatchEvent(new CustomEvent(VOICE_FAILED, { detail: why ? `${what}: ${why}` : what }))
}

// The mic's samples, as they come, to the page: an AudioWorklet, loaded
// from a Blob so the face stays static files.
const MIC_WORKLET = `
class Mic extends AudioWorkletProcessor {
  process(inputs) {
    const ch = inputs[0] && inputs[0][0]
    if (ch) this.port.postMessage(ch.slice(0))
    return true
  }
}
registerProcessor('mic', Mic)
`

function toBase64(pcm: Int16Array) {
  const bytes = new Uint8Array(pcm.buffer, pcm.byteOffset, pcm.byteLength)
  let s = ''
  for (let i = 0; i < bytes.length; i += 0x8000) s += String.fromCharCode(...bytes.subarray(i, i + 0x8000))
  return btoa(s)
}

function fromBase64(b64: string): Float32Array {
  const bin = atob(b64)
  const n = bin.length >> 1
  const out = new Float32Array(n)
  for (let i = 0; i < n; i++) {
    let v = bin.charCodeAt(2 * i) | (bin.charCodeAt(2 * i + 1) << 8)
    if (v >= 0x8000) v -= 0x10000
    out[i] = v / 0x8000
  }
  return out
}

type Event = { type?: string; [k: string]: unknown }

// What is known of the answer being spoken: its item, how much of its
// sound is queued, when it began to play, whether all of it has come.
type Answer = {
  item: string
  response?: string
  text: string
  started?: number // AudioContext time its first sound plays
  queued: number // seconds of its sound queued
  audioDone: boolean
  cut: boolean
}

// What the person picked, read when a call starts: the chat model, the ear
// and the voice (<mouth>_<voice>). They go in the session's own fields
// (audio.input.transcription.model, audio.output.voice), the chat model in
// chat_model, ours: the schema has none, its model being the realtime
// model. A realtime model that is one model ignores them; ours, built of
// parts, takes them. Empty: the agent's own.
export type CallPick = { chat?: string; stt?: string; voice?: string }

export class RealtimeCallAdapter implements RealtimeVoiceAdapter {
  private path: string
  private pick: () => CallPick

  constructor(path = '/api/v1/realtime', pick: () => CallPick = () => ({})) {
    this.path = path
    this.pick = pick
  }

  static isSupported() {
    return typeof WebSocket !== 'undefined' && typeof AudioWorkletNode !== 'undefined' && !!navigator.mediaDevices?.getUserMedia
  }

  connect(options: { abortSignal?: AbortSignal }): RealtimeVoiceAdapter.Session {
    const path = this.path
    const pick = this.pick()
    return createVoiceSession(options, async (h) => {
      const url = `${location.protocol === 'https:' ? 'wss:' : 'ws:'}//${location.host}${path}`
      let muted = false
      let hungUp = false
      let ctx: AudioContext | undefined
      let stream: MediaStream | undefined
      let ws: WebSocket | undefined
      const playing = new Set<AudioBufferSourceNode>()
      let next = 0 // AudioContext time the next sound starts
      let answer: Answer | undefined
      let mode: RealtimeVoiceAdapter.Mode = 'listening'

      const setMode = (m: RealtimeVoiceAdapter.Mode) => {
        if (m !== mode) h.emitMode((mode = m))
      }
      const send = (ev: object) => {
        if (ws?.readyState === WebSocket.OPEN) ws.send(JSON.stringify(ev))
      }
      const cleanup = () => {
        for (const s of playing) s.stop()
        playing.clear()
        stream?.getTracks().forEach((t) => t.stop())
        void ctx?.close().catch(() => {})
      }

      // The answer is cut where the listener is: what played of it stops,
      // and the model is told how much was heard.
      const cut = () => {
        if (!answer || answer.cut || !ctx) return
        answer.cut = true
        const heard = answer.started === undefined ? 0 : Math.max(0, Math.min(ctx.currentTime - answer.started, answer.queued))
        for (const s of playing) s.stop()
        playing.clear()
        next = ctx.currentTime
        send({ type: 'conversation.item.truncate', item_id: answer.item, content_index: 0, audio_end_ms: Math.round(heard * 1000) })
        setMode('listening')
      }
      const finish = () => {
        if (answer && answer.text) h.emitTranscript({ role: 'assistant', text: answer.text, isFinal: true })
      }
      const answering = (item: string, response?: string) => {
        if (answer?.item === item) return answer
        finish()
        answer = { item, response, text: '', queued: 0, audioDone: false, cut: false }
        return answer
      }

      const play = (a: Answer, b64: string) => {
        if (!ctx || a.cut) return
        const samples = fromBase64(b64)
        if (!samples.length) return
        const buf = ctx.createBuffer(1, samples.length, RATE)
        buf.copyToChannel(samples as Float32Array<ArrayBuffer>, 0)
        const src = ctx.createBufferSource()
        src.buffer = buf
        src.connect(ctx.destination)
        const at = Math.max(next, ctx.currentTime)
        if (a.started === undefined) a.started = at
        src.start(at)
        next = at + buf.duration
        a.queued += buf.duration
        playing.add(src)
        src.onended = () => {
          playing.delete(src)
          if (!playing.size) setMode('listening')
        }
        setMode('speaking')
      }

      const onEvent = (ev: Event) => {
        const item = typeof ev.item_id === 'string' ? ev.item_id : undefined
        const response = typeof ev.response_id === 'string' ? ev.response_id : undefined
        switch (ev.type) {
          case 'session.created':
            h.setStatus({ type: 'running' })
            h.emitMode('listening')
            break
          case 'input_audio_buffer.speech_started':
            if (playing.size) cut()
            break
          case 'conversation.item.input_audio_transcription.completed':
            if (typeof ev.transcript === 'string' && ev.transcript.trim())
              h.emitTranscript({ role: 'user', text: ev.transcript.trim(), isFinal: true })
            break
          case 'response.output_audio_transcript.delta':
            if (item && typeof ev.delta === 'string') {
              const a = answering(item, response)
              a.text += ev.delta
              h.emitTranscript({ role: 'assistant', text: a.text, isFinal: false })
            }
            break
          case 'response.output_audio_transcript.done':
            if (item && typeof ev.transcript === 'string') answering(item, response).text = ev.transcript
            break
          case 'response.output_audio.delta':
            if (item && typeof ev.delta === 'string') play(answering(item, response), ev.delta)
            break
          case 'response.output_audio.done':
            if (item) answering(item, response).audioDone = true
            break
          case 'response.done': {
            const r = (ev.response ?? {}) as { id?: string; status?: string }
            if (answer && answer.response === r.id) {
              // Done before its sound was all sent, or cancelled: interrupted.
              if (!answer.audioDone || r.status === 'cancelled') cut()
              finish()
              answer = undefined
            }
            break
          }
          case 'error': {
            const e = (ev.error ?? {}) as { message?: string }
            said('The call', e.message ?? 'an error')
            break
          }
        }
      }

      try {
        ctx = new AudioContext({ sampleRate: RATE })
        const worklet = URL.createObjectURL(new Blob([MIC_WORKLET], { type: 'text/javascript' }))
        await ctx.audioWorklet.addModule(worklet)
        URL.revokeObjectURL(worklet)
        stream = await navigator.mediaDevices.getUserMedia({
          audio: { echoCancellation: true, noiseSuppression: true, autoGainControl: true, channelCount: 1 },
        })
        if (h.isDisposed()) {
          cleanup()
          return { disconnect() {}, mute() {}, unmute() {} }
        }
        const mic = new AudioWorkletNode(ctx, 'mic', { numberOfOutputs: 0 })
        ctx.createMediaStreamSource(stream).connect(mic)
        let pending = new Float32Array(CHUNK)
        let filled = 0
        mic.port.onmessage = (e: MessageEvent<Float32Array>) => {
          let data = e.data
          while (data.length) {
            const take = Math.min(data.length, CHUNK - filled)
            pending.set(data.subarray(0, take), filled)
            filled += take
            data = data.subarray(take)
            if (filled < CHUNK) break
            let sum = 0
            const pcm = new Int16Array(CHUNK)
            for (let i = 0; i < CHUNK; i++) {
              const v = Math.max(-1, Math.min(1, pending[i]))
              sum += v * v
              pcm[i] = v < 0 ? v * 0x8000 : v * 0x7fff
            }
            h.emitVolume(muted ? 0 : Math.min(1, Math.sqrt(sum / CHUNK) * 4))
            if (!muted) send({ type: 'input_audio_buffer.append', audio: toBase64(pcm) })
            pending = new Float32Array(CHUNK)
            filled = 0
          }
        }
        await ctx.resume()
      } catch (error) {
        cleanup()
        said('No microphone', error)
        throw error
      }

      ws = new WebSocket(url)
      ws.onopen = () =>
        send({
          type: 'session.update',
          session: {
            type: 'realtime',
            output_modalities: ['audio'],
            ...(pick.chat ? { chat_model: pick.chat } : {}),
            audio: {
              input: {
                format: { type: 'audio/pcm', rate: RATE },
                transcription: pick.stt ? { model: pick.stt } : {},
                turn_detection: { type: 'server_vad' },
              },
              output: { format: { type: 'audio/pcm', rate: RATE }, ...(pick.voice ? { voice: pick.voice } : {}) },
            },
          },
        })
      ws.onmessage = (e) => {
        if (typeof e.data !== 'string') return
        try {
          onEvent(JSON.parse(e.data))
        } catch (error) {
          console.error('a realtime event not read', error)
        }
      }
      ws.onclose = (e) => {
        finish()
        cleanup()
        if (hungUp) return
        if (e.code === 1000) {
          h.end('finished')
          return
        }
        // A 401 at the door shows as a close before any session.
        const why = e.reason || `closed (${e.code})`
        said('The call ended', why)
        h.end('error', new Error(why))
      }

      return {
        disconnect() {
          hungUp = true
          ws?.close(1000, 'hung up')
          finish()
          cleanup()
        },
        mute() {
          muted = true
        },
        unmute() {
          muted = false
        },
      }
    })
  }
}
