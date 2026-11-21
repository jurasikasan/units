import { useState } from 'react'
import { GhostIcon } from 'lucide-react'
import { Avatar as AvatarCircle, AvatarFallback } from '@/upstream/components/ui/avatar'
import { cn } from '@/upstream/lib/utils'
import { holderLabel, initials, type Avatar } from '@/avatars'

type Props = {
  // The avatars the principal steers, each with who holds it now.
  avatars: Avatar[]
  // Who the person is on this device: '' the ghost, else an avatar's id.
  who: string
  pick: (id: string) => void
  profiles: string[]
  take: (id: string) => Promise<void>
  giveBack: (id: string) => Promise<void>
  hand: (id: string, profile: string) => Promise<void>
}

// Picker is the top of the sidebar: who the person is on this device. The
// ghost (nobody: the principal without a body, who sees every thread of
// their avatars and can do nothing), then each avatar they steer, with who
// holds its reins now (reins' CONTRACT.md, section 5). Under the avatar
// picked, the radio of its brains.
export function Picker({ avatars, who, pick, profiles, take, giveBack, hand }: Props) {
  const item = 'hover:bg-sidebar-accent flex w-full min-w-0 items-center gap-2 rounded-md px-2 py-1.5 text-left text-sm outline-none focus-visible:ring-2 focus-visible:ring-ring aria-checked:bg-sidebar-accent aria-checked:font-medium'
  return (
    <div role="radiogroup" aria-label="Who I am here" className="flex flex-col gap-0.5">
      <button type="button" role="radio" aria-checked={!who} aria-label="Ghost" className={item} onClick={() => pick('')}>
        <span className="bg-muted flex size-7 shrink-0 items-center justify-center rounded-full" aria-hidden>
          <GhostIcon className="size-4" />
        </span>
        <span className="grid min-w-0 flex-1 leading-tight">
          <span className="truncate">Ghost</span>
          <span className="text-muted-foreground truncate text-xs">sees all, does nothing</span>
        </span>
      </button>
      {avatars.map((a) => {
        const label = holderLabel(a.current_brain)
        const picked = who === a.id
        return (
          <div key={a.id} className="flex flex-col">
            <button type="button" role="radio" aria-checked={picked} aria-label={`${a.nick}: ${label}`} className={item} onClick={() => pick(a.id)}>
              <AvatarCircle className="size-7" aria-hidden>
                <AvatarFallback className="text-xs">{initials(a.name || a.nick)}</AvatarFallback>
              </AvatarCircle>
              <span className="grid min-w-0 flex-1 leading-tight">
                <span className="truncate">{a.name || a.nick}</span>
                <span className={cn('truncate text-xs', a.current_brain?.here ? 'text-primary' : 'text-muted-foreground')}>{label}</span>
              </span>
            </button>
            {picked && <Brains avatar={a} profiles={profiles} take={take} giveBack={giveBack} hand={hand} />}
          </div>
        )
      })}
    </div>
  )
}

// Brains is the radio under the avatar picked: who is in it. None (give
// back), this device (take: the page writes as it), or a brain profile
// reins lists (hand: the page watches it answer). Held by another device
// or another steward, that is said, nothing is checked, and picking an
// option still takes it over: stewards are equal.
function Brains({ avatar, profiles, take, giveBack, hand }: { avatar: Avatar; profiles: string[] } & Pick<Props, 'take' | 'giveBack' | 'hand'>) {
  const [refused, setRefused] = useState('')
  const [busy, setBusy] = useState(false)
  const b = avatar.current_brain
  const checked = !b || b.kind === 'offline' ? 'none' : b.kind === 'brain' ? `brain:${b.profile}` : b.here ? 'here' : ''
  const options = [
    { value: 'none', label: 'none', act: () => giveBack(avatar.id) },
    { value: 'here', label: 'this device', act: () => take(avatar.id) },
    ...profiles.map((p) => ({ value: `brain:${p}`, label: p, act: () => hand(avatar.id, p) })),
  ]
  const choose = async (act: () => Promise<void>) => {
    setRefused('')
    setBusy(true)
    try {
      await act()
    } catch (e) {
      setRefused(e instanceof Error ? e.message : String(e))
    } finally {
      setBusy(false)
    }
  }
  return (
    <fieldset className="border-sidebar-border mt-0.5 mb-1 ml-5 flex flex-col gap-0.5 border-l py-0.5 pl-3" aria-busy={busy}>
      <legend className="sr-only">{`${avatar.nick}'s brain`}</legend>
      {!checked && <p className="text-muted-foreground py-0.5 text-xs">held by {holderLabel(b)}</p>}
      {options.map((o) => (
        <label key={o.value} className="hover:bg-sidebar-accent flex cursor-pointer items-center gap-2 rounded-md px-1.5 py-1 text-sm">
          <input
            type="radio"
            name={`brain-${avatar.id}`}
            value={o.value}
            checked={checked === o.value}
            disabled={busy}
            onChange={() => void choose(o.act)}
            className="accent-primary size-3.5"
          />
          {o.label}
        </label>
      ))}
      {refused && (
        <p role="alert" className="text-destructive text-xs">
          {refused}
        </p>
      )}
    </fieldset>
  )
}
