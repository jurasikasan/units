import { useCallback, useEffect, useMemo, useRef, useState } from 'react'
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
import { ComposerContext, ComposerRow } from '@/Composer'
import { Holders } from '@/Holders'
import { withDevice } from '@/device'
import { useWorld } from '@/world'
import { SidebarInset, SidebarProvider, SidebarTrigger } from '@/upstream/components/ui/sidebar'

// A 401 means the door in front no longer knows who this is: reloading the
// page lets it ask. The face itself knows no door.
const fetchOrLogIn: typeof fetch = async (input, init) => {
  const resp = await fetch(input, init)
  if (resp.status === 401) window.location.reload()
  return resp
}

// reins is told which device asks: a principal holds an avatar's reins
// from a device (src/device.ts).
const fetchReins = withDevice(fetchOrLogIn)

// The avatar the person last chose to speak as on this device, among those
// held here.
const SPEAK_AS = 'units.speakAs'
const kept = () => {
  try {
    return localStorage.getItem(SPEAK_AS) ?? undefined
  } catch {
    return undefined
  }
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
  const { avatars, save, reload, held, take, giveBack, hand, profiles } = useAvatars(fetchReins)
  const [speakAs, setSpeakAs] = useState(kept)
  const chooseSpeakAs = useCallback((id: string) => {
    setSpeakAs(id)
    try {
      localStorage.setItem(SPEAK_AS, id)
    } catch {
      // kept for as long as the page is open
    }
  }, [])
  const wired = wiring(avatars, me?.preferred_username, speakAs)
  // The login's avatar is taken up on load when nobody holds it: a person
  // who comes is in it. One somebody holds (a brain, another device) is
  // left as it is: taking it is the person's choice.
  const tookUp = useRef(false)
  useEffect(() => {
    if (tookUp.current || !avatars || !me?.preferred_username) return
    tookUp.current = true
    const login = avatars.find((a) => a.nick === me.preferred_username)
    if (login && (login.current_brain?.kind ?? 'offline') === 'offline') take(login.id).catch(() => {})
  }, [avatars, me, take])
  // The adapters read sound at each call; a change saved in the settings is
  // in the next request.
  Object.assign(sound, picks(wired))
  const [settings, setSettings] = useState(false)
  const listen = useMemo(() => ({ hold: held, ready: () => void reload() }), [held, reload])
  const world = useWorld(fetchReins, wired.me, wired.other, avatars, listen)
  // Stop shows beside a brain that holds one of the person's avatars.
  const stoppable = useCallback((sender: string) => avatars?.find((a) => a.id === sender)?.current_brain?.kind === 'brain', [avatars])
  const runtime = useExternalStoreRuntime({
    messages: world.messages,
    convertMessage: asIs,
    // People talk over each other: a reply being written never holds the
    // composer.
    isRunning: false,
    isLoading: !world.ready,
    onNew: world.say,
    onVoiceTranscript: world.heard,
    adapters: { attachments, dictation, speech, voice: call, threadList: world.threadList },
  })

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <TooltipProvider>
        <SidebarProvider>
          <ChatSidebar me={me && <Me me={me} onSettings={avatars ? () => setSettings(true) : undefined} />} />
          <SidebarInset className="h-dvh">
            <header className="flex shrink-0 items-start gap-2 border-b px-2 py-1.5">
              <SidebarTrigger />
              {avatars && (
                <Holders members={world.members} avatars={avatars} me={wired.me} profiles={profiles} take={take} giveBack={giveBack} hand={hand} speakAs={chooseSpeakAs} />
              )}
            </header>
            <div className="min-h-0 flex-1">
              <ComposerContext.Provider value={{ call: !!call, writers: world.writers, stoppable, stop: world.stop }}>
                <Thread components={{ ComposerExtra: ComposerRow }} />
              </ComposerContext.Provider>
            </div>
          </SidebarInset>
        </SidebarProvider>
        {avatars && <Settings open={settings} onOpenChange={setSettings} avatars={avatars} first={wired.me} offers={offers} save={save} />}
        <VoiceNotice />
      </TooltipProvider>
    </AssistantRuntimeProvider>
  )
}
