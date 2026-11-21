import { useState, type FC, type PropsWithChildren } from 'react'
import type { ToolCallMessagePartComponent } from '@assistant-ui/react'
import {
  ToolFallbackArgs,
  ToolFallbackContent,
  ToolFallbackResult,
  ToolFallbackRoot,
  ToolFallbackTrigger,
} from '@/upstream/components/assistant-ui/elements/tool-fallback.aui'

// What a tool returned, as the face keeps it on a tool-call part
// (src/world.ts): its words, and the pictures and sounds it made.
export type ToolMedia = { type: string; url: string; media_type: string }
export type ToolResult = { text?: string; media?: ToolMedia[] }

const asResult = (r: unknown): ToolResult => (r && typeof r === 'object' ? (r as ToolResult) : typeof r === 'string' ? { text: r } : {})

// toolLabel is how a tool is named on its card: the mind names it
// server__tool ("images__draw"), shown as "images · draw".
export const toolLabel = (name: string) => name.split('__').join(' · ')

// ToolCard is an avatar's tool call inside its message: which tool, what
// it was asked (the arguments, a prompt shown as it is), and what the tool
// returned — a picture or a sound shown on the card, under the tool's
// name, never as the avatar's own words. assistant-ui's tool fallback
// (upstream) is its frame: the trigger says which tool and how it went,
// and opens the arguments and the tool's words.
export const ToolCard: ToolCallMessagePartComponent = ({ toolName, args, argsText, result, isError, status }) => {
  const [open, setOpen] = useState(false)
  const r = asResult(result)
  const media = r.media ?? []
  const prompt = typeof args?.prompt === 'string' ? args.prompt : undefined
  const label = toolLabel(toolName)
  return (
    <div data-slot="tool-card" role="group" aria-label={`Tool ${label}`} className="my-2 w-fit max-w-full rounded-lg border px-3 py-1">
      <ToolFallbackRoot open={open} onOpenChange={setOpen}>
        <ToolFallbackTrigger toolName={label} status={status} isError={isError} />
        <ToolFallbackContent>
          <ToolFallbackArgs argsText={argsText} />
          <ToolFallbackResult result={r.text || undefined} />
        </ToolFallbackContent>
      </ToolFallbackRoot>
      {prompt && <p className="text-muted-foreground pb-1 text-sm italic">“{prompt}”</p>}
      {media.map((m, i) =>
        m.type === 'image' ? (
          <figure key={i} className="pb-2">
            <img src={m.url} alt={prompt ?? `a picture from ${label}`} className="max-h-96 max-w-full rounded-md" />
            <figcaption className="text-muted-foreground pt-1 text-xs">from {label}</figcaption>
          </figure>
        ) : m.type === 'audio' ? (
          <figure key={i} className="pb-2">
            <audio controls src={m.url} aria-label={`a sound from ${label}`} />
            <figcaption className="text-muted-foreground pt-1 text-xs">from {label}</figcaption>
          </figure>
        ) : null,
      )}
    </div>
  )
}

// ToolCalls shows an avatar's tool calls as they stand, each its own card,
// rather than folded under "Used n tools": what a tool returned is part of
// what the avatar did.
export const ToolCalls: FC<PropsWithChildren> = ({ children }) => <>{children}</>
