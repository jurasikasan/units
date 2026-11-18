import { MicIcon, MicOffIcon, PhoneIcon, PhoneOffIcon } from 'lucide-react'
import { useVoiceControls, useVoiceState } from '@assistant-ui/react'
import { TooltipIconButton } from '@/upstream/components/assistant-ui/elements/tooltip-icon-button'

// Call: the buttons of a call, in the composer's row (Thread's
// ComposerExtra slot; upstream's thread has none). Start, then mute or
// unmute and hang up while it goes; what is said lands in the thread.
// Shown only where the runtime has a realtime adapter (src/realtime.ts).
export function Call() {
  const voice = useVoiceState()
  const { connect, disconnect, mute, unmute } = useVoiceControls()
  const live = voice !== undefined && voice.status.type !== 'ended'
  const cls = 'size-7 rounded-full'
  if (!live)
    return (
      <TooltipIconButton tooltip="Start a call" side="bottom" type="button" variant="ghost" size="icon" className={`${cls} text-muted-foreground hover:text-foreground`} aria-label="Start a call" onClick={connect}>
        <PhoneIcon className="size-4" />
      </TooltipIconButton>
    )
  return (
    <span className="flex items-center gap-1" data-call={voice.status.type} data-mode={voice.mode}>
      {voice.isMuted ? (
        <TooltipIconButton tooltip="Unmute" side="bottom" type="button" variant="ghost" size="icon" className={`${cls} text-destructive`} aria-label="Unmute" onClick={unmute}>
          <MicOffIcon className="size-4" />
        </TooltipIconButton>
      ) : (
        <TooltipIconButton tooltip="Mute" side="bottom" type="button" variant="ghost" size="icon" className={`${cls} text-muted-foreground hover:text-foreground`} aria-label="Mute" onClick={mute}>
          <MicIcon className={`size-4 ${voice.mode === 'listening' ? 'animate-pulse' : ''}`} />
        </TooltipIconButton>
      )}
      <TooltipIconButton tooltip="Hang up" side="bottom" type="button" variant="destructive" size="icon" className={cls} aria-label="Hang up" onClick={disconnect}>
        <PhoneOffIcon className="size-4" />
      </TooltipIconButton>
      <span className="text-muted-foreground text-xs" aria-live="polite">
        {voice.status.type === 'starting' ? 'calling…' : voice.mode === 'speaking' ? 'speaking' : 'listening'}
      </span>
    </span>
  )
}
