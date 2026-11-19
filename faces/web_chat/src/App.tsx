import { useEffect, useMemo, useState } from 'react'
import { HttpAgent } from '@ag-ui/client'
import { AssistantRuntimeProvider } from '@assistant-ui/react'
import { useAgUiRuntime } from '@assistant-ui/react-ag-ui'
import { Thread } from '@/upstream/components/assistant-ui/elements/thread.aui'
import { TooltipProvider } from '@/upstream/components/ui/tooltip'
import { attachments } from '@/attachments'
import { SpeechEndpointAdapter, TranscriptionDictationAdapter } from '@/voice'
import { VoiceNotice } from '@/VoiceNotice'
import { RealtimeCallAdapter } from '@/realtime'
import { Call } from '@/Call'
import { Models, useModels } from '@/Models'

// A 401 means the door in front no longer knows who this is: reloading the
// page lets it ask. The face itself knows no door.
const fetchOrLogIn: typeof fetch = async (input, init) => {
  const resp = await fetch(input, init)
  if (resp.status === 401) window.location.reload()
  return resp
}

const TRANSCRIBE = '/api/v1/audio/transcriptions'
const SPEAK = '/api/v1/audio/speech'
const CALL = '/api/v1/realtime'

// served is whether something answers POST at path: asked with HEAD, a
// route that exists says "not this method" (405), one that does not says
// 404. The mic, read-aloud and a call show only where they would be answered
// (a call is a GET, so asked with HEAD it is refused, not missing).
async function served(path: string) {
  try {
    const resp = await fetchOrLogIn(path, { method: 'HEAD' })
    return resp.status !== 404
  } catch {
    return false
  }
}

function useVoice() {
  const [voice, setVoice] = useState<{
    dictation?: TranscriptionDictationAdapter
    speech?: SpeechEndpointAdapter
    call?: RealtimeCallAdapter
  }>({})
  useEffect(() => {
    let live = true
    Promise.all([served(TRANSCRIBE), served(SPEAK), served(CALL)]).then(([ear, mouth, line]) => {
      if (!live) return
      setVoice({
        dictation: ear && TranscriptionDictationAdapter.isSupported() ? new TranscriptionDictationAdapter(TRANSCRIBE) : undefined,
        speech: mouth ? new SpeechEndpointAdapter(SPEAK) : undefined,
        call: line && RealtimeCallAdapter.isSupported() ? new RealtimeCallAdapter(CALL) : undefined,
      })
    })
    return () => {
      live = false
    }
  }, [])
  return voice
}

// The face keeps one conversation, in memory, and sends it whole with each
// run to whatever answers AG-UI at /api/run, attachments included, with the
// model picked among those offered at /api/v1/models (src/Models.tsx). Sound
// goes to OpenAI's audio API on the same origin (src/voice.ts), and a call
// to its Realtime API there (src/realtime.ts).
export default function App() {
  const agent = useMemo(() => new HttpAgent({ url: '/api/run', fetch: fetchOrLogIn }), [])
  const { dictation, speech, call } = useVoice()
  const models = useModels(fetchOrLogIn)
  const runtime = useAgUiRuntime({ agent, adapters: { attachments, dictation, speech, voice: call } })
  // The composer's row: the model to answer, and a call where one is served.
  const ComposerExtra = useMemo(
    () =>
      function ComposerExtra() {
        return (
          <>
            <Models models={models} />
            {call ? <Call /> : null}
          </>
        )
      },
    [models, call],
  )

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <TooltipProvider>
        <main className="h-dvh">
          <Thread components={{ ComposerExtra }} />
        </main>
        <VoiceNotice />
      </TooltipProvider>
    </AssistantRuntimeProvider>
  )
}
