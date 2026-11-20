import { useState } from 'react'
import { Avatar as AvatarCircle, AvatarFallback } from '@/upstream/components/ui/avatar'
import { Button } from '@/upstream/components/ui/button'
import { Popover, PopoverContent, PopoverTrigger } from '@/upstream/components/ui/popover'
import { cn } from '@/upstream/lib/utils'
import { holderLabel, initials, type Avatar } from '@/avatars'

type Props = {
  // Who is in the chat shown (subject ids).
  members: string[]
  // The principal's avatars, each with who holds it now.
  avatars: Avatar[]
  // The avatar the page writes as; none: it only watches.
  me?: Avatar
  profiles: string[]
  take: (id: string) => Promise<void>
  giveBack: (id: string) => Promise<void>
  hand: (id: string, profile: string) => Promise<void>
  speakAs: (id: string) => void
}

// Holders is the row above the chat, in the page's flow: each avatar in
// it, and who holds its reins now — you here, you on another device,
// another steward, a brain (a driver's profile), or nobody. Shown to the
// principal only: the characters never see it (reins' CONTRACT.md, section
// 5). An avatar of the principal's opens what can be done with its reins:
// take them from this device, give them back, hand them to a brain, or,
// held here, speak as it. Holding none here, the page watches.
export function Holders({ members, avatars, me, profiles, take, giveBack, hand, speakAs }: Props) {
  const [refused, setRefused] = useState('')
  const mine = new Map(avatars.map((a) => [a.id, a]))
  const shown = members.length ? members : avatars.map((a) => a.id)
  const doing = (f: () => Promise<void> | void) => async () => {
    setRefused('')
    try {
      await f()
    } catch (e) {
      setRefused(e instanceof Error ? e.message : String(e))
    }
  }
  return (
    <div role="group" aria-label="Who holds the reins" className="flex min-w-0 flex-1 flex-wrap items-center gap-1.5">
      {shown.map((id) => {
        const a = mine.get(id)
        if (!a) {
          return (
            <span key={id} className="text-muted-foreground px-1 text-xs">
              someone else's
            </span>
          )
        }
        const b = a.current_brain
        const label = holderLabel(b)
        const speaking = me?.id === a.id
        return (
          <Popover key={id}>
            <PopoverTrigger
              aria-label={`${a.nick}: ${label}`}
              className={cn(
                'hover:bg-accent flex max-w-56 items-center gap-1.5 rounded-full border px-1.5 py-0.5 text-xs',
                speaking && 'border-primary bg-primary/5',
              )}
            >
              <AvatarCircle className="size-5" aria-hidden>
                <AvatarFallback className="text-[10px]">{initials(a.name || a.nick)}</AvatarFallback>
              </AvatarCircle>
              <span className="truncate font-medium">{a.name || a.nick}</span>
              <span className={cn('truncate', b?.here ? 'text-primary' : 'text-muted-foreground')}>{speaking ? 'you write as it' : label}</span>
            </PopoverTrigger>
            <PopoverContent align="start" className="w-60 gap-1 p-1.5" aria-label={`${a.nick}'s reins`}>
              <p className="text-muted-foreground px-2 py-1 text-xs">
                {a.nick}: held by {label}
              </p>
              {b?.here && !speaking && (
                <Button variant="ghost" size="sm" className="justify-start" onClick={doing(() => speakAs(a.id))}>
                  Speak as {a.name || a.nick}
                </Button>
              )}
              {!b?.here && (
                <Button variant="ghost" size="sm" className="justify-start" onClick={doing(() => take(a.id))}>
                  Take the reins here
                </Button>
              )}
              {b && b.kind !== 'offline' && (
                <Button variant="ghost" size="sm" className="justify-start" onClick={doing(() => giveBack(a.id))}>
                  Give back
                </Button>
              )}
              {profiles
                .filter((p) => !(b?.kind === 'brain' && b.profile === p))
                .map((p) => (
                  <Button key={p} variant="ghost" size="sm" className="justify-start" onClick={doing(() => hand(a.id, p))}>
                    Hand to {p}
                  </Button>
                ))}
            </PopoverContent>
          </Popover>
        )
      })}
      {!me && (
        <span role="status" aria-label="Watching" className="text-muted-foreground px-1 text-xs">
          watching: take the reins of an avatar to write
        </span>
      )}
      {refused && (
        <span role="alert" className="text-destructive truncate px-1 text-xs">
          {refused}
        </span>
      )}
    </div>
  )
}
