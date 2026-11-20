import type { ReactNode } from 'react'
import { Sidebar, SidebarContent, SidebarFooter, SidebarRail } from '@/upstream/components/ui/sidebar'
import { ThreadList } from '@/upstream/components/assistant-ui/elements/thread-list.aui'

// The page's sidebar, put together as assistant-ui's threadlist-sidebar is
// (shadcn's sidebar, its thread list), without that one's own branding: the
// chats, and at the very foot who is logged in, which opens the settings.
export function ChatSidebar({ me }: { me?: ReactNode }) {
  return (
    <Sidebar>
      <SidebarContent className="px-2 pt-2">
        <ThreadList />
      </SidebarContent>
      {me && <SidebarFooter className="border-t" aria-label="Who is logged in">{me}</SidebarFooter>}
      <SidebarRail />
    </Sidebar>
  )
}
