import type { ReactNode } from 'react'
import { Sidebar, SidebarContent, SidebarFooter, SidebarRail } from '@/upstream/components/ui/sidebar'
import { ThreadList } from '@/upstream/components/assistant-ui/elements/thread-list.aui'

// The page's sidebar, put together as assistant-ui's threadlist-sidebar is
// (shadcn's sidebar, its thread list), without that one's own branding: the
// chats, below them what a run is asked with (the selectors), and at the
// very foot who is logged in. Those settings move to the subject's page
// once there is one.
export function ChatSidebar({ settings, me }: { settings: ReactNode; me?: ReactNode }) {
  return (
    <Sidebar>
      <SidebarContent className="px-2 pt-2">
        <ThreadList />
      </SidebarContent>
      <SidebarFooter className="border-t" aria-label="Settings">
        {settings}
      </SidebarFooter>
      {me && <SidebarFooter className="border-t" aria-label="Who is logged in">{me}</SidebarFooter>}
      <SidebarRail />
    </Sidebar>
  )
}
