import { useEffect, useState } from 'react'
import { ModelSelector, type ModelOption } from '@/upstream/components/assistant-ui/elements/model-selector.aui'
import { ModelSelectorContent, ModelSelectorRoot, ModelSelectorTrigger } from '@/upstream/components/assistant-ui/elements/model-selector'

const MODELS = '/api/v1/models'

// One model the agent offers, from OpenAI's GET /v1/models on the same
// origin; mode and voices are the mind's fields beside OpenAI's (chat when
// not said).
export type Offer = { id: string; mode: string; voices: string[] }

// What the person picked for sound; the voice adapters read it at each
// call. Empty: the agent's own.
export type SoundPick = { stt?: string; tts?: string; voice?: string }

// useModels is what the agent offers; [] when nothing answers there, and
// then nothing is picked.
export function useModels(fetcher: typeof fetch) {
  const [offers, setOffers] = useState<Offer[]>([])
  useEffect(() => {
    let live = true
    fetcher(MODELS)
      .then((r) => (r.ok ? r.json() : { data: [] }))
      .then((list: { data?: { id: string; mode?: string; voices?: string[] }[] }) => {
        if (live) setOffers((list.data ?? []).map((m) => ({ id: m.id, mode: m.mode || 'chat', voices: m.voices ?? [] })))
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [fetcher])
  return offers
}

const options = (ids: string[]): ModelOption[] => ids.map((id) => ({ id, name: id }))

function Row({ label, children }: { label: string; children: React.ReactNode }) {
  return (
    <div className="flex items-center justify-between gap-2">
      <span className="text-muted-foreground text-xs">{label}</span>
      {children}
    </div>
  )
}

// Pick is one of the agent's lists, the first chosen, outside the
// runtime: what it is for is the caller's.
function Pick({ label, ids, value, onChange }: { label: string; ids: string[]; value?: string; onChange: (v: string) => void }) {
  if (ids.length === 0) return null
  return (
    <Row label={label}>
      <ModelSelectorRoot models={options(ids)} value={value ?? ids[0]} onValueChange={onChange}>
        <ModelSelectorTrigger variant="ghost" size="sm" aria-label={`${label}: ${value ?? ids[0]}`} />
        <ModelSelectorContent />
      </ModelSelectorRoot>
    </Row>
  )
}

// Models: the pickers in the sidebar, each list's first chosen. The chat
// model is assistant-ui's own model selector: its pick goes into the
// runtime's model context, which the AG-UI runtime sends as the run's
// forwardedProps (modelName). Speech to text, text to speech and the voice
// go into sound, which the voice adapters send as OpenAI's model and voice;
// a new speech model brings its first voice, the one shown.
export function Models({ offers, sound }: { offers: Offer[]; sound: SoundPick }) {
  const [, redraw] = useState(0)
  const set = (p: SoundPick) => {
    Object.assign(sound, p)
    redraw((n) => n + 1)
  }
  const of = (mode: string) => offers.filter((o) => o.mode === mode)
  const chat = options(of('chat').map((o) => o.id))
  const ears = of('audio_transcription').map((o) => o.id)
  const mouths = of('audio_speech')
  const mouth = mouths.find((o) => o.id === sound.tts) ?? mouths[0]
  return (
    <div className="flex flex-col gap-1">
      {chat.length > 0 && (
        <Row label="Model">
          <ModelSelector models={chat} defaultValue={chat[0].id} variant="ghost" size="sm" />
        </Row>
      )}
      <Pick label="Speech to text" ids={ears} value={sound.stt} onChange={(stt) => set({ stt })} />
      <Pick label="Text to speech" ids={mouths.map((o) => o.id)} value={sound.tts} onChange={(tts) => set({ tts, voice: mouths.find((o) => o.id === tts)?.voices[0] })} />
      <Pick label="Voice" ids={mouth?.voices ?? []} value={sound.voice} onChange={(voice) => set({ voice })} />
    </div>
  )
}
