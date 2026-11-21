import { useCallback, useEffect, useMemo, useState } from 'react'
import { AssistantRuntimeProvider, useExternalStoreRuntime, type ThreadMessageLike } from '@assistant-ui/react'
import { Thread } from '@/upstream/components/assistant-ui/elements/thread.aui'
import { TooltipProvider } from '@/upstream/components/ui/tooltip'
import { attachments } from '@/attachments'
import { SpeechEndpointAdapter, TranscriptionDictationAdapter } from '@/voice'
import { Notice } from '@/Notice'
import { notify } from '@/notice'
import { RealtimeCallAdapter } from '@/realtime'
import { useModels, type SoundPick } from '@/Models'
import { ChatSidebar } from '@/Sidebar'
import { Picker } from '@/Picker'
import { Me, useMe } from '@/Me'
import { Settings } from '@/Settings'
import { holderLabel, picks, useAvatars, wiring } from '@/avatars'
import { ComposerContext, ComposerRow, Watching } from '@/Composer'
import { Said } from '@/Said'
import { ToolCalls, ToolCard } from '@/ToolCard'
import { withDevice } from '@/device'
import { useWho } from '@/who'
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

// The face is organised around who the person is on this device
// (src/who.ts, src/Picker.tsx): the ghost by default, the principal without
// a body, who sees every thread of their avatars (as reins shows a
// principal) and can do nothing; or one of the avatars they steer, seeing
// the threads it is in, writing as it while its reins are held here. Who
// holds each avatar, and the radio that takes, gives back or hands its
// reins, are in the picker at the top of the sidebar; leaving an avatar
// held here gives it back. The world is read from reins (src/world.ts),
// followed as one stream, and kept nowhere: assistant-ui's external store
// runtime is fed from reins, and a reload reads it all again. Who answers
// is whoever holds the other avatar's reins (a driver, or a person); the
// face never asks a model itself. Who is logged in sits at the sidebar's
// foot (src/Me.tsx), which opens the settings: the avatars' profiles and
// bodies (src/Settings.tsx). Their ears and voices, among the models
// offered at /api/v1/models (src/Models.tsx), are what the mic, read-aloud
// and a call are asked with. Sound goes to OpenAI's audio API on the same
// origin (src/voice.ts), and a call to its Realtime API there
// (src/realtime.ts).
export default function App() {
  const sound = useMemo<SoundPick>(() => ({}), [])
  const { dictation, speech, call } = useVoice(sound)
  const offers = useModels(fetchOrLogIn)
  const me = useMe(fetchOrLogIn)
  const { avatars, save, reload, held, take, giveBack, hand, profiles } = useAvatars(fetchReins)
  const [kept, keep] = useWho()
  // An avatar no longer steered (or not read yet) is nobody: the ghost.
  const who = avatars?.some((a) => a.id === kept) ? kept : ''
  const wired = wiring(avatars, who)
  // Leaving an avatar (another picked, or the ghost) gives it back when
  // this device holds it, to nobody; held by anyone else, it is left as it
  // is.
  const pick = useCallback(
    (id: string) => {
      if (id === who) return
      const leaving = avatars?.find((a) => a.id === who)
      if (leaving?.current_brain?.here) giveBack(leaving.id).catch((e) => notify(`Not given back: ${(e as Error).message}`))
      keep(id)
    },
    [who, avatars, giveBack, keep],
  )
  // The adapters read sound at each call; a change saved in the settings is
  // in the next request.
  Object.assign(sound, picks(wired))
  const [settings, setSettings] = useState(false)
  const listen = useMemo(() => ({ hold: held, ready: () => void reload() }), [held, reload])
  const world = useWorld(fetchReins, wired.me, wired.writes, wired.other, avatars, listen)
  // Stop shows beside a brain that holds one of the person's avatars; never
  // for the ghost, who can do nothing.
  const inside = !!wired.me
  const stoppable = useCallback((sender: string) => inside && avatars?.find((a) => a.id === sender)?.current_brain?.kind === 'brain', [avatars, inside])
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
  const watching = !wired.me
    ? 'You are the ghost here: you see every thread of your avatars and can do nothing. Pick an avatar to step in.'
    : wired.writes
      ? undefined
      : `${wired.me.name || wired.me.nick} is held by ${holderLabel(wired.me.current_brain)}. Pick "this device" under it to write as it.`
  const shownAs = wired.me ? wired.me.name || wired.me.nick : 'Ghost'

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <TooltipProvider>
        <SidebarProvider>
          <ChatSidebar
            acts={wired.writes}
            picker={avatars && <Picker avatars={avatars} who={who} pick={pick} profiles={profiles} take={take} giveBack={giveBack} hand={hand} />}
            me={me && <Me me={me} onSettings={avatars ? () => setSettings(true) : undefined} />}
          />
          <SidebarInset className="h-dvh min-w-0">
            <header className="flex shrink-0 items-center gap-2 border-b px-2 py-1.5">
              <SidebarTrigger />
              <span className="text-muted-foreground truncate text-sm" aria-label="Who I am here">
                {shownAs}
              </span>
            </header>
            <div className="min-h-0 flex-1">
              <ComposerContext.Provider value={{ call: !!call, writers: world.writers, stoppable, stop: world.stop, watching }}>
                <Thread components={{ ComposerExtra: ComposerRow, AssistantMessageHeader: Said, ToolFallback: ToolCard, ToolGroup: ToolCalls, ...(watching ? { ComposerInstead: Watching } : {}) }} />
              </ComposerContext.Provider>
            </div>
          </SidebarInset>
        </SidebarProvider>
        {avatars && <Settings open={settings} onOpenChange={setSettings} avatars={avatars} first={wired.me} offers={offers} save={save} />}
        <Notice />
      </TooltipProvider>
    </AssistantRuntimeProvider>
  )
}
