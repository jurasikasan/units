import { useEffect, useState } from 'react'
import type { ModelOption } from '@/upstream/components/assistant-ui/elements/model-selector'
import { ModelSelectorContent, ModelSelectorRoot, ModelSelectorTrigger } from '@/upstream/components/assistant-ui/elements/model-selector'

const MODELS = '/api/v1/models'

// One model the agent offers, from OpenAI's GET /v1/models on the same
// origin; mode and voices are the mind's fields beside OpenAI's (chat when
// not said).
export type Offer = { id: string; mode: string; voices: string[] }

// What the mic, read-aloud and a call are asked with: the avatars' bodies
// (src/avatars.ts); the adapters read it at each call. Empty: the agent's
// own.
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

// Pick is one of the agent's lists, assistant-ui's model selector without
// the runtime (its props-driven elements), the first shown when nothing is
// picked: the agent's own. What it is for is the caller's.
export function Pick({ id, label, ids, value, onChange, variant = 'outline' }: { id?: string; label: string; ids: string[]; value?: string; onChange: (v: string) => void; variant?: 'outline' | 'ghost' }) {
  if (ids.length === 0) return null
  return (
    <ModelSelectorRoot models={options(ids)} value={value ?? ids[0]} onValueChange={onChange}>
      <ModelSelectorTrigger id={id} variant={variant} size="sm" className="w-fit" aria-label={`${label}: ${value ?? ids[0]}`} />
      <ModelSelectorContent />
    </ModelSelectorRoot>
  )
}
