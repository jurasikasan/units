import { useEffect, useState } from 'react'
import { LogOutIcon, Settings2Icon } from 'lucide-react'
import { Avatar, AvatarFallback } from '@/upstream/components/ui/avatar'
import { SidebarMenu, SidebarMenuButton, SidebarMenuItem } from '@/upstream/components/ui/sidebar'
import { initials } from '@/avatars'
import { TooltipIconButton } from '@/upstream/components/assistant-ui/elements/tooltip-icon-button'

const USERINFO = '/api/v1/userinfo'

// Who is logged in, as the door in front tells the page: OpenID Connect's
// UserInfo claims, and end_session_endpoint (OIDC's name, from its
// discovery) beside them for where logging out happens.
export type UserInfo = { sub: string; preferred_username?: string; name?: string; email?: string; end_session_endpoint?: string }

// useMe is who is logged in; undefined when nothing answers there.
export function useMe(fetcher: typeof fetch) {
  const [me, setMe] = useState<UserInfo>()
  useEffect(() => {
    let live = true
    fetcher(USERINFO)
      .then((r) => (r.ok ? r.json() : undefined))
      .then((u?: UserInfo) => {
        if (live && u?.sub) setMe(u)
      })
      .catch(() => {})
    return () => {
      live = false
    }
  }, [fetcher])
  return me
}

// Me: the foot of the sidebar, as shadcn's own sidebar blocks have it: who
// is logged in, and the way out when the door names one. Where there are
// avatars to show, the row opens the settings (src/Settings.tsx).
export function Me({ me, onSettings }: { me: UserInfo; onSettings?: () => void }) {
  const name = me.name || me.preferred_username || me.sub
  const detail = me.email || (name !== me.sub ? me.sub : undefined)
  const out = me.end_session_endpoint
  const who = (
    <>
      <Avatar className="rounded-lg">
        <AvatarFallback className="rounded-lg">{initials(name)}</AvatarFallback>
      </Avatar>
      <div className="grid min-w-0 flex-1 text-left text-sm leading-tight" title={me.sub}>
        <span className="truncate font-medium">{name}</span>
        {detail && <span className="text-muted-foreground truncate text-xs">{detail}</span>}
      </div>
    </>
  )
  return (
    <SidebarMenu>
      <SidebarMenuItem className="flex items-center gap-2 px-1 py-1">
        {onSettings ? (
          <SidebarMenuButton size="lg" className="min-w-0 flex-1 px-0" onClick={onSettings} aria-label="Settings" title="Settings">
            {who}
            <Settings2Icon className="text-muted-foreground" />
          </SidebarMenuButton>
        ) : (
          who
        )}
        {out && (
          <TooltipIconButton tooltip="Log out" side="top" variant="ghost" onClick={() => window.location.assign(out)}>
            <LogOutIcon />
          </TooltipIconButton>
        )}
      </SidebarMenuItem>
    </SidebarMenu>
  )
}
