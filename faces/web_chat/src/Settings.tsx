import { useEffect, useRef, useState } from 'react'
import { PlusIcon } from 'lucide-react'
import { Breadcrumb, BreadcrumbItem, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from '@/upstream/components/ui/breadcrumb'
import { Avatar as AvatarCircle, AvatarFallback } from '@/upstream/components/ui/avatar'
import { Button } from '@/upstream/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/upstream/components/ui/dialog'
import { Field, FieldDescription, FieldError, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/upstream/components/ui/field'
import { Input } from '@/upstream/components/ui/input'
import { Textarea } from '@/upstream/components/ui/textarea'
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupAction,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
} from '@/upstream/components/ui/sidebar'
import { Pick, type Offer } from '@/Models'
import { initials, nickOf, NICK, Refused, type Avatar, type Change, type NewAvatar } from '@/avatars'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  avatars: Avatar[]
  first?: Avatar
  offers: Offer[]
  save: (id: string, change: Change) => Promise<unknown>
  create: (n: NewAvatar) => Promise<Avatar>
}

// Settings: the avatars the principal steers, each its profile and its
// preferences, in shadcn's "sidebar in a dialog" block (sidebar-13): the
// avatars on the left, the one chosen on the right. The nick is shown and
// never changes; name, bio and preferences are saved to reins. Every
// avatar has the same: an ear, a mouth and its voice, whoever is in it.
// Which model thinks is not the body's: the brain holding its reins is.
//
// The "+" beside "Avatars" makes a new one: a "New" item at the list's
// foot, and an empty profile in place of an avatar's. Once made, the new
// avatar's own form opens, with its body.
export function Settings({ open, onOpenChange, avatars, first, offers, save, create }: Props) {
  const [chosen, setChosen] = useState<string>()
  const [making, setMaking] = useState(false)
  const shown = making ? undefined : (avatars.find((a) => a.id === chosen) ?? first ?? avatars[0])
  const choose = (id: string) => {
    setMaking(false)
    setChosen(id)
  }
  const nav = (
    <SidebarMenu>
      {avatars.map((a) => (
        <SidebarMenuItem key={a.id}>
          <SidebarMenuButton isActive={a.id === shown?.id} onClick={() => choose(a.id)}>
            <AvatarCircle className="size-5 rounded-md" aria-hidden>
              <AvatarFallback className="rounded-md text-[10px]">{initials(a.name || a.nick)}</AvatarFallback>
            </AvatarCircle>
            <span>{a.nick}</span>
          </SidebarMenuButton>
        </SidebarMenuItem>
      ))}
      {making && (
        <SidebarMenuItem>
          <SidebarMenuButton isActive>
            <AvatarCircle className="size-5 rounded-md" aria-hidden>
              <AvatarFallback className="rounded-md">
                <PlusIcon className="size-3" />
              </AvatarFallback>
            </AvatarCircle>
            <span>New</span>
          </SidebarMenuButton>
        </SidebarMenuItem>
      )}
    </SidebarMenu>
  )
  // The group is drawn twice: in the block's sidebar from md up, above the
  // form on a phone. The "+" goes with it, in the label's row.
  const group = (className?: string) => (
    <SidebarGroup className={className}>
      <SidebarGroupLabel>Avatars</SidebarGroupLabel>
      <SidebarGroupAction aria-label="New avatar" title="New avatar" className="touch-manipulation select-none [-webkit-tap-highlight-color:transparent] active:scale-90" onClick={() => setMaking(true)}>
        <PlusIcon />
      </SidebarGroupAction>
      <SidebarGroupContent>{nav}</SidebarGroupContent>
    </SidebarGroup>
  )
  // Closed, a half-made avatar is dropped: the dialog opens on one that is.
  const openChange = (o: boolean) => {
    if (!o) setMaking(false)
    onOpenChange(o)
  }
  return (
    <Dialog open={open} onOpenChange={openChange}>
      <DialogContent className="overflow-hidden p-0 md:max-h-[560px] md:max-w-[700px] lg:max-w-[800px]">
        <DialogTitle className="sr-only">Settings</DialogTitle>
        <DialogDescription className="sr-only">Your avatars: their profiles and preferences.</DialogDescription>
        <SidebarProvider className="min-h-0 items-start">
          <Sidebar collapsible="none" className="hidden md:flex" aria-label="Avatars">
            <SidebarContent>{group()}</SidebarContent>
          </Sidebar>
          <main className="flex h-[85dvh] max-h-[85dvh] flex-1 flex-col overflow-hidden md:h-[540px]">
            <header className="flex h-16 shrink-0 items-center gap-2 px-4">
              <Breadcrumb>
                <BreadcrumbList>
                  <BreadcrumbItem className="hidden md:block">Settings</BreadcrumbItem>
                  <BreadcrumbSeparator className="hidden md:block" />
                  <BreadcrumbItem>
                    <BreadcrumbPage className="truncate">{making ? 'New avatar' : (shown?.nick ?? 'No avatars')}</BreadcrumbPage>
                  </BreadcrumbItem>
                </BreadcrumbList>
              </Breadcrumb>
            </header>
            <div className="flex flex-1 flex-col overflow-y-auto p-4 pt-0 pb-6">
              {/* On a phone the block's nav is hidden: the avatars go above,
                  scrolling with the form, so many of them never squeeze it
                  out of the dialog. */}
              {group('-mx-2 -mt-2 mb-2 md:hidden')}
              {making ? (
                <NewAvatarForm create={create} made={(a) => choose(a.id)} />
              ) : (
                shown && <AvatarForm key={shown.id} avatar={shown} offers={offers} save={save} />
              )}
            </div>
          </main>
        </SidebarProvider>
      </DialogContent>
    </Dialog>
  )
}

// AvatarForm is one avatar: its profile, its preferences, a Save that
// sends what changed. A list's first is shown when nothing is set, and
// that is the agent's own.
function AvatarForm({ avatar, offers, save }: { avatar: Avatar; offers: Offer[]; save: Props['save'] }) {
  const prefs = avatar.preferences ?? {}
  const [name, setName] = useState(avatar.name)
  const [bio, setBio] = useState(avatar.bio ?? '')
  const [picked, setPicked] = useState<Record<string, string>>({})
  const [state, setState] = useState<{ saving?: boolean; saved?: boolean; error?: string }>({})

  const of = (mode: string) => offers.filter((o) => o.mode === mode)
  const ears = of('audio_transcription').map((o) => o.id)
  const mouths = of('audio_speech')
  const value = (key: string) => picked[key] ?? (prefs[key] as string | undefined)
  const mouth = mouths.find((o) => o.id === value('mouth')) ?? mouths[0]
  const voice = value('voice') ?? mouth?.voices[0]

  const pick = (key: string) => (v: string) => setPicked((p) => ({ ...p, [key]: v }))
  // A new mouth brings its first voice; a mouth and its voice are saved
  // together, so a voice never goes with another mouth's model.
  const pickMouth = (id: string) => setPicked((p) => ({ ...p, mouth: id, voice: mouths.find((o) => o.id === id)?.voices[0] ?? '' }))
  const pickVoice = (v: string) => setPicked((p) => ({ ...p, mouth: mouth?.id ?? '', voice: v }))

  const change: Change = {}
  if (name !== avatar.name) change.name = name
  if (bio !== (avatar.bio ?? '')) change.bio = bio
  const changedPrefs = Object.fromEntries(
    Object.entries(picked)
      .filter(([k, v]) => v !== prefs[k])
      .map(([k, v]) => [k, v === '' ? null : v]),
  )
  if (Object.keys(changedPrefs).length > 0) change.preferences = changedPrefs
  const dirty = Object.keys(change).length > 0

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    setState({ saving: true })
    try {
      await save(avatar.id, change)
      setPicked({})
      setState({ saved: true })
    } catch (err) {
      setState({ error: err instanceof Error ? err.message : String(err) })
    }
  }

  const fid = (k: string) => `${avatar.id}-${k}`
  return (
    <form onSubmit={submit} aria-label={`Avatar ${avatar.nick}`} className="flex max-w-xl flex-col gap-6">
      <FieldSet>
        <FieldLegend>Profile</FieldLegend>
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor={fid('nick')}>Nick</FieldLabel>
            <Input id={fid('nick')} value={avatar.nick} readOnly disabled />
            <FieldDescription>How others address this avatar. It never changes.</FieldDescription>
          </Field>
          <Field>
            <FieldLabel htmlFor={fid('name')}>Name</FieldLabel>
            <Input id={fid('name')} value={name} onChange={(e) => setName(e.target.value)} placeholder={avatar.nick} />
          </Field>
          <Field>
            <FieldLabel htmlFor={fid('bio')}>Bio</FieldLabel>
            <Textarea id={fid('bio')} value={bio} onChange={(e) => setBio(e.target.value)} placeholder="Who this avatar is" />
          </Field>
        </FieldGroup>
      </FieldSet>

      {(ears.length > 0 || mouths.length > 0) && (
        <FieldSet>
          <FieldLegend>Body</FieldLegend>
          <FieldDescription>How this avatar hears and how it sounds, whoever is in it. The first of each list is the agent's own.</FieldDescription>
          <FieldGroup>
            {ears.length > 0 && (
              <Field orientation="horizontal">
                <FieldLabel htmlFor={fid('ear')}>Speech to text</FieldLabel>
                <Pick id={fid('ear')} label="Speech to text" ids={ears} value={value('ear')} onChange={pick('ear')} />
              </Field>
            )}
            {mouths.length > 0 && (
              <Field orientation="horizontal">
                <FieldLabel htmlFor={fid('mouth')}>Text to speech</FieldLabel>
                <Pick id={fid('mouth')} label="Text to speech" ids={mouths.map((o) => o.id)} value={mouth?.id} onChange={pickMouth} />
              </Field>
            )}
            {mouth && (
              <Field orientation="horizontal">
                <FieldLabel htmlFor={fid('voice')}>Voice</FieldLabel>
                {mouth.voices.length > 0 ? (
                  <Pick id={fid('voice')} label="Voice" ids={mouth.voices} value={voice} onChange={pickVoice} />
                ) : (
                  // A speech model whose engine lists no voices (not
                  // reachable, or one voice only): it speaks with its own.
                  <span id={fid('voice')} className="text-muted-foreground text-sm">
                    {mouth.id} lists none: its own
                  </span>
                )}
              </Field>
            )}
          </FieldGroup>
        </FieldSet>
      )}

      <div className="flex items-center gap-3">
        <Button type="submit" size="sm" disabled={!dirty || state.saving}>
          {state.saving ? 'Saving…' : 'Save'}
        </Button>
        {state.saved && !dirty && <span className="text-muted-foreground text-sm" role="status">Saved</span>}
        {state.error && <span className="text-destructive text-sm" role="alert">{state.error}</span>}
      </div>
    </form>
  )
}

// What the form says when reins refuses a nick, in plain words.
const NICK_RULE = 'A nick is 2 to 32 characters: a to z, 0 to 9, - or _, starting with a letter.'
const refusal = (err: unknown) =>
  err instanceof Refused && err.status === 409
    ? { nick: 'That nick is taken. Try another.' }
    : err instanceof Refused && err.status === 422
      ? { nick: NICK_RULE }
      : { error: 'The avatar could not be created. Try again in a moment.' }

// NewAvatarForm is an avatar's profile, empty: name, nick, bio. The nick
// follows the name until it is typed by hand; it is set once and never
// changes. No body here: the avatar's own form shows it once it exists.
function NewAvatarForm({ create, made }: { create: Props['create']; made: (a: Avatar) => void }) {
  const [name, setName] = useState('')
  const [typed, setTyped] = useState<string>()
  const [bio, setBio] = useState('')
  const [state, setState] = useState<{ creating?: boolean; nick?: string; error?: string }>({})
  const nick = typed ?? nickOf(name)
  // On a phone the avatars sit above in the same scroll: with many of
  // them, the empty form opens below the fold. Brought into view, the
  // name first.
  const top = useRef<HTMLFormElement>(null)
  // Braced: scrollIntoView returns a promise in newer browsers, and an
  // effect must return nothing or its cleanup.
  useEffect(() => {
    top.current?.scrollIntoView({ block: 'nearest' })
  }, [])

  // Why it cannot be created yet, said beside the button.
  const why = !nick
    ? name.trim() && typed === undefined
      ? 'Type a nick: this name has no Latin letters to make one from.'
      : 'Type a nick to create it.'
    : NICK.test(nick)
      ? undefined
      : NICK_RULE

  const submit = async (e: React.FormEvent) => {
    e.preventDefault()
    if (why || state.creating) return
    setState({ creating: true })
    try {
      made(await create({ nick, name: name.trim(), bio: bio.trim() }))
    } catch (err) {
      setState(refusal(err))
    }
  }

  const fid = (k: string) => `new-avatar-${k}`
  return (
    <form ref={top} onSubmit={submit} aria-label="New avatar" className="flex max-w-xl scroll-mt-4 flex-col gap-6" noValidate>
      <FieldSet>
        <FieldLegend>Profile</FieldLegend>
        <FieldGroup>
          <Field>
            <FieldLabel htmlFor={fid('name')}>Name</FieldLabel>
            <Input id={fid('name')} value={name} onChange={(e) => setName(e.target.value)} placeholder="Scribe" autoComplete="off" enterKeyHint="go" autoFocus />
          </Field>
          <Field data-invalid={!!state.nick || undefined}>
            <FieldLabel htmlFor={fid('nick')}>Nick</FieldLabel>
            <Input
              id={fid('nick')}
              value={nick}
              onChange={(e) => {
                setTyped(e.target.value.toLowerCase())
                if (state.nick) setState({})
              }}
              maxLength={32}
              autoComplete="off"
              autoCapitalize="none"
              enterKeyHint="go"
              autoCorrect="off"
              spellCheck={false}
              aria-invalid={!!state.nick || undefined}
              aria-describedby={`${fid('nick-about')}${state.nick ? ` ${fid('nick-error')}` : ''}`}
            />
            <FieldDescription id={fid('nick-about')}>How others address it. Can't be changed later.</FieldDescription>
            {state.nick && <FieldError id={fid('nick-error')}>{state.nick}</FieldError>}
          </Field>
          <Field>
            <FieldLabel htmlFor={fid('bio')}>Bio</FieldLabel>
            <Textarea id={fid('bio')} value={bio} onChange={(e) => setBio(e.target.value)} placeholder="Who this avatar is" />
          </Field>
        </FieldGroup>
      </FieldSet>

      <div className="flex flex-wrap items-center gap-x-3 gap-y-2">
        <Button type="submit" size="sm" disabled={!!why || state.creating} aria-describedby={why ? fid('why') : undefined}>
          {state.creating ? 'Creating…' : 'Create avatar'}
        </Button>
        {why && (
          <span id={fid('why')} className="text-muted-foreground text-sm">
            {why}
          </span>
        )}
        {state.error && (
          <span className="text-destructive text-sm" role="alert">
            {state.error}
          </span>
        )}
      </div>
    </form>
  )
}
