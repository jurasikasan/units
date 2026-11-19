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
import { Models, useModels, type SoundPick } from '@/Models'
import { ChatSidebar } from '@/Sidebar'
import { useThreads } from '@/threads'
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/upstream/components/ui/sidebar'

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

function useVoice(sound: SoundPick) {
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
        dictation: ear && TranscriptionDictationAdapter.isSupported() ? new TranscriptionDictationAdapter(TRANSCRIBE, () => ({ model: sound.stt })) : undefined,
        speech: mouth ? new SpeechEndpointAdapter(SPEAK, () => ({ model: sound.tts, voice: sound.voice })) : undefined,
        call: line && RealtimeCallAdapter.isSupported() ? new RealtimeCallAdapter(CALL) : undefined,
      })
    })
    return () => {
      live = false
    }
  }, [sound])
  return voice
}

// The face keeps its conversations in memory, and sends the current one whole with each
// run to whatever answers AG-UI at /api/run, attachments included, with the
// model picked among those offered at /api/v1/models (src/Models.tsx); the
// page's chats and the pickers sit in a sidebar (src/Sidebar.tsx,
// src/threads.ts). Sound
// goes to OpenAI's audio API on the same origin (src/voice.ts), and a call
// to its Realtime API there (src/realtime.ts).
export default function App() {
  const agent = useMemo(() => new HttpAgent({ url: '/api/run', fetch: fetchOrLogIn, threadId: crypto.randomUUID() }), [])
  const sound = useMemo<SoundPick>(() => ({}), [])
  const { dictation, speech, call } = useVoice(sound)
  const offers = useModels(fetchOrLogIn)
  const threads = useThreads(agent)
  const runtime = useAgUiRuntime({ agent, adapters: { attachments, dictation, speech, voice: call, threadList: threads.adapter } })
  const { bind } = threads
  useEffect(() => bind(runtime), [bind, runtime])

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <TooltipProvider>
        <SidebarProvider>
          <ChatSidebar settings={<Models offers={offers} sound={sound} />} />
          <SidebarInset className="h-dvh">
            <SidebarTrigger className="absolute top-2 left-2 z-10" />
            <Thread components={call ? { ComposerExtra: Call } : undefined} />
          </SidebarInset>
        </SidebarProvider>
        <VoiceNotice />
      </TooltipProvider>
    </AssistantRuntimeProvider>
  )
}
