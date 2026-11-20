import { useEffect, useMemo, useState } from 'react'
import { AssistantRuntimeProvider, useExternalStoreRuntime, type ThreadMessageLike } from '@assistant-ui/react'
import { Thread } from '@/upstream/components/assistant-ui/elements/thread.aui'
import { TooltipProvider } from '@/upstream/components/ui/tooltip'
import { attachments } from '@/attachments'
import { SpeechEndpointAdapter, TranscriptionDictationAdapter } from '@/voice'
import { VoiceNotice } from '@/VoiceNotice'
import { RealtimeCallAdapter } from '@/realtime'
import { useModels, type SoundPick } from '@/Models'
import { ChatSidebar } from '@/Sidebar'
import { Me, useMe } from '@/Me'
import { Settings } from '@/Settings'
import { picks, useAvatars, wiring } from '@/avatars'
import { CallContext, ComposerRow } from '@/Composer'
import { useWorld } from '@/world'
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
        call: line && RealtimeCallAdapter.isSupported()
          ? new RealtimeCallAdapter(CALL, () => ({
              stt: sound.stt,
              voice: sound.tts && sound.voice ? `${sound.tts}_${sound.voice}` : undefined,
            }))
          : undefined,
      })
    })
    return () => {
      live = false
    }
  }, [sound])
  return voice
}

const asIs = (m: ThreadMessageLike) => m

// The face shows the world as reins lets the person see it (src/world.ts):
// the threads their avatars are in, read from reins, followed as one stream,
// and what they say appended there as the avatar named by their login. It
// keeps nothing: assistant-ui's external store runtime is fed from reins,
// and a reload reads it all again. Who answers is whoever holds the other
// avatar's reins (a driver, or a person); the face never asks a model
// itself. The chats sit in a sidebar (src/Sidebar.tsx), who is logged in at
// its foot (src/Me.tsx), which opens the settings: the avatars the
// principal steers, from reins (src/avatars.ts, src/Settings.tsx). Their
// bodies' ears and voices, among the models offered at /api/v1/models
// (src/Models.tsx), are what the mic, read-aloud and a call are asked
// with. Sound goes to OpenAI's audio API on the same origin (src/voice.ts),
// and a call to its Realtime API there (src/realtime.ts).
export default function App() {
  const sound = useMemo<SoundPick>(() => ({}), [])
  const { dictation, speech, call } = useVoice(sound)
  const offers = useModels(fetchOrLogIn)
  const me = useMe(fetchOrLogIn)
  const { avatars, save } = useAvatars(fetchOrLogIn)
  const wired = wiring(avatars, me?.preferred_username)
  // The adapters read sound at each call; a change saved in the settings is
  // in the next request.
  Object.assign(sound, picks(wired))
  const [settings, setSettings] = useState(false)
  const world = useWorld(fetchOrLogIn, wired.me, wired.other, avatars)
  const runtime = useExternalStoreRuntime({
    messages: world.messages,
    convertMessage: asIs,
    // People talk over each other: a reply being written never holds the
    // composer.
    isRunning: false,
    isLoading: !world.ready,
    onNew: world.say,
    adapters: { attachments, dictation, speech, voice: call, threadList: world.threadList },
  })

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <TooltipProvider>
        <SidebarProvider>
          <ChatSidebar me={me && <Me me={me} onSettings={avatars ? () => setSettings(true) : undefined} />} />
          <SidebarInset className="h-dvh">
            <SidebarTrigger className="absolute top-2 left-2 z-10" />
            <CallContext.Provider value={!!call}>
              <Thread components={{ ComposerExtra: ComposerRow }} />
            </CallContext.Provider>
          </SidebarInset>
        </SidebarProvider>
        {avatars && <Settings open={settings} onOpenChange={setSettings} avatars={avatars} first={wired.me} offers={offers} save={save} />}
        <VoiceNotice />
      </TooltipProvider>
    </AssistantRuntimeProvider>
  )
}
