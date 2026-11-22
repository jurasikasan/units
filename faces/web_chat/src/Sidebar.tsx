import { useState, type MouseEvent, type ReactNode } from 'react'
import { useAuiState } from '@assistant-ui/react'
import { Sidebar, SidebarContent, SidebarFooter, SidebarHeader, SidebarRail, useSidebar } from '@/upstream/components/ui/sidebar'
import { ThreadListItems, ThreadListNew, ThreadListRoot, ThreadListSearch } from '@/upstream/components/assistant-ui/elements/thread-list.aui'
import { cn } from '@/upstream/lib/utils'

// The page's sidebar, put together as assistant-ui's threadlist-sidebar is
// (shadcn's sidebar, its thread list), without that one's own branding. At
// the top, who the person is on this device (src/Picker.tsx); then the
// threads seen from there, a filter by chat; at the very foot who is logged
// in, which opens the settings. New thread, and a thread's menu (rename,
// delete), are there only with an avatar to act as: held here.
export function ChatSidebar({ picker, me, acts }: { picker?: ReactNode; me?: ReactNode; acts: boolean }) {
  const { isMobile, setOpenMobile } = useSidebar()
  const [search, setSearch] = useState('')
  const hasThreads = useAuiState((s) => s.threads.threadIds.length > 0)
  // On a phone the sidebar is a sheet: a thread picked, or a new one,
  // closes it and shows the thread.
  const closeOnPick = (e: MouseEvent) => {
    if (isMobile && (e.target as HTMLElement).closest('[data-slot="aui_thread-list-item-trigger"], [data-slot="aui_thread-list-new"]')) setOpenMobile(false)
  }
  return (
    <Sidebar>
      {picker && (
        // Bounded, and scrolled on its own: with many avatars (made from
        // the settings' "+") the picker would push the threads and the
        // foot, the way to the settings, out of the sidebar.
        <SidebarHeader className="max-h-[45svh] overflow-y-auto overscroll-contain border-b">
          {picker}
        </SidebarHeader>
      )}
      <SidebarContent className="px-2 pt-2" onClickCapture={closeOnPick}>
        <ThreadListRoot className={cn(!acts && '[&_[data-slot$=item-more]]:hidden')}>
          {acts && <ThreadListNew />}
          {hasThreads && <ThreadListSearch value={search} onValueChange={setSearch} />}
          <ThreadListItems searchQuery={hasThreads ? search : ''} />
        </ThreadListRoot>
      </SidebarContent>
      {me && <SidebarFooter className="border-t" aria-label="Who is logged in">{me}</SidebarFooter>}
      <SidebarRail />
    </Sidebar>
  )
}
