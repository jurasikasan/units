import { useEffect, useState } from 'react'
import { ModelSelector, type ModelOption } from '@/upstream/components/assistant-ui/elements/model-selector.aui'

const MODELS = '/api/v1/models'

// The models the agent offers, from OpenAI's GET /v1/models on the same
// origin, the one a run without a pick goes to first; [] when nothing
// answers there, and then nothing is picked.
export function useModels(fetcher: typeof fetch) {
  const [models, setModels] = useState<ModelOption[]>([])
  useEffect(() => {
    let live = true
    fetcher(MODELS)
      .then((r) => (r.ok ? r.json() : { data: [] }))
      .then((list: { data?: { id: string }[] }) => {
        if (live) setModels((list.data ?? []).map((m) => ({ id: m.id, name: m.id })))
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [fetcher])
  return models
}

// Models: assistant-ui's model selector, the first offered chosen. The pick
// goes into the runtime's model context, which the AG-UI runtime sends as
// the run's forwardedProps (modelName).
export function Models({ models }: { models: ModelOption[] }) {
  if (models.length === 0) return null
  return <ModelSelector models={models} defaultValue={models[0].id} variant="ghost" size="sm" />
}
