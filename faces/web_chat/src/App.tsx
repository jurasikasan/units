import { useMemo } from 'react'
import { HttpAgent } from '@ag-ui/client'
import { AssistantRuntimeProvider } from '@assistant-ui/react'
import { useAgUiRuntime } from '@assistant-ui/react-ag-ui'
import { Thread } from '@/upstream/components/assistant-ui/elements/thread.aui'
import { TooltipProvider } from '@/upstream/components/ui/tooltip'
import { attachments } from '@/attachments'

// A 401 means the door in front no longer knows who this is: reloading the
// page lets it ask. The face itself knows no door.
const fetchOrLogIn: typeof fetch = async (input, init) => {
  const resp = await fetch(input, init)
  if (resp.status === 401) window.location.reload()
  return resp
}

// The face keeps one conversation, in memory, and sends it whole with each
// run to whatever answers AG-UI at /api/run, attachments included.
export default function App() {
  const agent = useMemo(() => new HttpAgent({ url: '/api/run', fetch: fetchOrLogIn }), [])
  const runtime = useAgUiRuntime({ agent, adapters: { attachments } })

  return (
    <AssistantRuntimeProvider runtime={runtime}>
      <TooltipProvider>
        <main className="h-dvh">
          <Thread />
        </main>
      </TooltipProvider>
    </AssistantRuntimeProvider>
  )
}
