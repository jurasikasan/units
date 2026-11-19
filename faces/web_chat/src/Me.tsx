import { useEffect, useState } from 'react'
import { LogOutIcon } from 'lucide-react'
import { Avatar, AvatarFallback } from '@/upstream/components/ui/avatar'
import { SidebarMenu, SidebarMenuItem } from '@/upstream/components/ui/sidebar'
import { TooltipIconButton } from '@/upstream/components/assistant-ui/elements/tooltip-icon-button'

const USERINFO = '/api/v1/userinfo'

// Who is logged in, as the door in front tells the page: OpenID Connect's
// UserInfo claims, and end_session_endpoint (OIDC's name, from its
// discovery) beside them for where logging out happens.
type UserInfo = { sub: string; preferred_username?: string; name?: string; email?: string; end_session_endpoint?: string }

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

const initials = (s: string) =>
  s
    .split(/[\s._@-]+/)
    .filter(Boolean)
    .slice(0, 2)
    .map((w) => w[0]!.toUpperCase())
    .join('')

// Me: the foot of the sidebar, as shadcn's own sidebar blocks have it: who
// is logged in, and the way out when the door names one.
export function Me({ me }: { me: UserInfo }) {
  const name = me.name || me.preferred_username || me.sub
  const detail = me.email || (name !== me.sub ? me.sub : undefined)
  const out = me.end_session_endpoint
  return (
    <SidebarMenu>
      <SidebarMenuItem className="flex items-center gap-2 px-1 py-1">
        <Avatar className="rounded-lg">
          <AvatarFallback className="rounded-lg">{initials(name)}</AvatarFallback>
        </Avatar>
        <div className="grid min-w-0 flex-1 text-left text-sm leading-tight" title={me.sub}>
          <span className="truncate font-medium">{name}</span>
          {detail && <span className="text-muted-foreground truncate text-xs">{detail}</span>}
        </div>
        {out && (
          <TooltipIconButton tooltip="Log out" side="top" variant="ghost" onClick={() => window.location.assign(out)}>
            <LogOutIcon />
          </TooltipIconButton>
        )}
      </SidebarMenuItem>
    </SidebarMenu>
  )
}
