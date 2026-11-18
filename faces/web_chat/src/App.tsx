import { useEffect, useMemo, useState } from 'react'
import { HttpAgent } from '@ag-ui/client'
import { AssistantRuntimeProvider } from '@assistant-ui/react'
import { useAgUiRuntime } from '@assistant-ui/react-ag-ui'
import { Thread } from '@/upstream/components/assistant-ui/elements/thread.aui'
import { TooltipProvider } from '@/upstream/components/ui/tooltip'
import { attachments } from '@/attachments'
import { SpeechEndpointAdapter, TranscriptionDictationAdapter } from '@/voice'
import { VoiceNotice } from '@/VoiceNotice'

// A 401 means the door in front no longer knows who this is: reloading the
// page lets it ask. The face itself knows no door.
const fetchOrLogIn: typeof fetch = async (input, init) => {
  const resp = await fetch(input, init)
  if (resp.status === 401) window.location.reload()
  return resp
}

const TRANSCRIBE = '/api/v1/audio/transcriptions'
const SPEAK = '/api/v1/audio/speech'

// served is whether something answers POST at path: asked with HEAD, a
// route that exists says "not this method" (405), one that does not says
// 404. The mic and read-aloud show only where they would be answered.
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
  }>({})
  useEffect(() => {
    let live = true
    Promise.all([served(TRANSCRIBE), served(SPEAK)]).then(([ear, mouth]) => {
      if (!live) return
      setVoice({
        dictation: ear && TranscriptionDictationAdapter.isSupported() ? new TranscriptionDictationAdapter(TRANSCRIBE) : undefined,
        speech: mouth ? new SpeechEndpointAdapter(SPEAK) : undefined,
      })
    })
    return () => {
      live = false
    }
  }, [])
  return voice
}

// The face keeps one conversation, in memory, and sends it whole with each
// run to whatever answers AG-UI at /api/run, attachments included. Sound
// goes to OpenAI's audio API on the same origin (src/voice.ts).
export default function App() {
  const agent = useMemo(() => new HttpAgent({ url: '/api/run', fetch: fetchOrLogIn }), [])
  const { dictation, speech } = useVoice()
  const runtime = useAgUiRuntime({ agent, adapters: { attachments, dictation, speech } })

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <TooltipProvider>
        <main className="h-dvh">
          <Thread />
        </main>
        <VoiceNotice />
      </TooltipProvider>
    </AssistantRuntimeProvider>
  )
}
