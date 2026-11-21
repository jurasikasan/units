import { useAuiState } from '@assistant-ui/react'
import { Avatar as AvatarCircle, AvatarFallback } from '@/upstream/components/ui/avatar'
import { initials } from '@/avatars'

// when says a message's time: the hour today, the day and hour before.
function when(at: Date) {
  const today = new Date().toDateString() === at.toDateString()
  return today ? at.toLocaleTimeString([], { hour: '2-digit', minute: '2-digit' }) : at.toLocaleString([], { day: 'numeric', month: 'short', hour: '2-digit', minute: '2-digit' })
}

// Said is Thread's AssistantMessageHeader slot (patches/06): above every
// message of the other side, who said it, their avatar's circle and name,
// and when. For the ghost that is every message.
export function Said() {
  const name = useAuiState((s) => {
    const custom = s.message.metadata.custom as { senderName?: string } | undefined
    return custom?.senderName || 'someone'
  })
  const at = useAuiState((s) => s.message.createdAt)
  return (
    <div data-slot="said-by" className="text-muted-foreground mb-1 flex items-center gap-2 px-2 text-xs">
      <AvatarCircle className="size-6" aria-hidden>
        <AvatarFallback className="text-[10px]">{initials(name)}</AvatarFallback>
      </AvatarCircle>
      <span className="text-foreground truncate font-medium">{name}</span>
      {at && !Number.isNaN(at.getTime()) && <time dateTime={at.toISOString()}>{when(at)}</time>}
    </div>
  )
}
