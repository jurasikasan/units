import { useState } from 'react'
import { Breadcrumb, BreadcrumbItem, BreadcrumbList, BreadcrumbPage, BreadcrumbSeparator } from '@/upstream/components/ui/breadcrumb'
import { Avatar as AvatarCircle, AvatarFallback } from '@/upstream/components/ui/avatar'
import { Button } from '@/upstream/components/ui/button'
import { Dialog, DialogContent, DialogDescription, DialogTitle } from '@/upstream/components/ui/dialog'
import { Field, FieldDescription, FieldGroup, FieldLabel, FieldLegend, FieldSet } from '@/upstream/components/ui/field'
import { Input } from '@/upstream/components/ui/input'
import { Textarea } from '@/upstream/components/ui/textarea'
import {
  Sidebar,
  SidebarContent,
  SidebarGroup,
  SidebarGroupContent,
  SidebarGroupLabel,
  SidebarMenu,
  SidebarMenuButton,
  SidebarMenuItem,
  SidebarProvider,
} from '@/upstream/components/ui/sidebar'
import { Pick, type Offer } from '@/Models'
import { initials, type Avatar, type Change } from '@/avatars'

type Props = {
  open: boolean
  onOpenChange: (open: boolean) => void
  avatars: Avatar[]
  first?: Avatar
  offers: Offer[]
  save: (id: string, change: Change) => Promise<unknown>
}

// Settings: the avatars the principal steers, each its profile and its
// preferences, in shadcn's "sidebar in a dialog" block (sidebar-13): the
// avatars on the left, the one chosen on the right. The nick is shown and
// never changes; name, bio and preferences are saved to reins. Every
// avatar has the same: an ear, a mouth and its voice, whoever is in it.
// Which model thinks is not the body's (src/Brain.tsx).
export function Settings({ open, onOpenChange, avatars, first, offers, save }: Props) {
  const [chosen, setChosen] = useState<string>()
  const shown = avatars.find((a) => a.id === chosen) ?? first ?? avatars[0]
  const nav = (
    <SidebarMenu>
      {avatars.map((a) => (
        <SidebarMenuItem key={a.id}>
          <SidebarMenuButton isActive={a.id === shown?.id} onClick={() => setChosen(a.id)}>
            <AvatarCircle className="size-5 rounded-md" aria-hidden>
              <AvatarFallback className="rounded-md text-[10px]">{initials(a.name || a.nick)}</AvatarFallback>
            </AvatarCircle>
            <span>{a.nick}</span>
          </SidebarMenuButton>
        </SidebarMenuItem>
      ))}
    </SidebarMenu>
  )
  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="overflow-hidden p-0 md:max-h-[560px] md:max-w-[700px] lg:max-w-[800px]">
        <DialogTitle className="sr-only">Settings</DialogTitle>
        <DialogDescription className="sr-only">Your avatars: their profiles and preferences.</DialogDescription>
        <SidebarProvider className="min-h-0 items-start">
          <Sidebar collapsible="none" className="hidden md:flex" aria-label="Avatars">
            <SidebarContent>
              <SidebarGroup>
                <SidebarGroupLabel>Avatars</SidebarGroupLabel>
                <SidebarGroupContent>{nav}</SidebarGroupContent>
              </SidebarGroup>
            </SidebarContent>
          </Sidebar>
          <main className="flex h-[540px] max-h-[85dvh] flex-1 flex-col overflow-hidden">
            <header className="flex h-16 shrink-0 items-center gap-2 px-4">
              <Breadcrumb>
                <BreadcrumbList>
                  <BreadcrumbItem className="hidden md:block">Settings</BreadcrumbItem>
                  <BreadcrumbSeparator className="hidden md:block" />
                  <BreadcrumbItem>
                    <BreadcrumbPage>{shown?.nick ?? 'No avatars'}</BreadcrumbPage>
                  </BreadcrumbItem>
                </BreadcrumbList>
              </Breadcrumb>
            </header>
            {/* On a phone the block's nav is hidden: the avatars go above. */}
            <div className="px-2 pb-2 md:hidden">{nav}</div>
            <div className="flex flex-1 flex-col overflow-y-auto p-4 pt-0 pb-6">
              {shown && <AvatarForm key={shown.id} avatar={shown} offers={offers} save={save} />}
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

      <div className="sticky bottom-0 -mb-6 flex items-center gap-3 py-3">
        <Button type="submit" size="sm" disabled={!dirty || state.saving}>
          {state.saving ? 'Saving…' : 'Save'}
        </Button>
        {state.saved && !dirty && <span className="text-muted-foreground text-sm" role="status">Saved</span>}
        {state.error && <span className="text-destructive text-sm" role="alert">{state.error}</span>}
      </div>
    </form>
  )
}
