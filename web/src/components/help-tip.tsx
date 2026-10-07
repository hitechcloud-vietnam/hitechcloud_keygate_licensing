import { HelpCircle } from "lucide-react"
import { cn } from "@/lib/utils"

/**
 * A question mark that shows its explanation on hover or keyboard
 * focus — for the paragraph that only matters to the person setting
 * a thing up, which would otherwise sit under the field and be read
 * by everyone else instead.
 *
 * Not the native `title` attribute: that takes about a second to
 * appear and renders as an OS tooltip, so people hover, see only the
 * help cursor, and move on.
 *
 * The text keeps its line breaks (whitespace-pre-line), so a couple
 * of short paragraphs read better here than one long sentence.
 */
export function HelpTip({ text, className }: { text: string; className?: string }) {
  return (
    <span className={cn("group relative inline-flex", className)}>
      <button
        type="button"
        aria-label={text}
        className="text-muted-foreground transition-colors hover:text-foreground focus-visible:text-foreground focus-visible:outline-none"
      >
        <HelpCircle className="h-3.5 w-3.5" />
      </button>
      <span
        role="tooltip"
        className="pointer-events-none invisible absolute left-1/2 top-full z-50 mt-1.5 w-[min(22rem,70vw)] -translate-x-1/2 whitespace-pre-line rounded-md border bg-popover p-3 text-xs font-normal leading-relaxed text-popover-foreground opacity-0 shadow-md transition-opacity group-hover:visible group-hover:opacity-100 group-focus-within:visible group-focus-within:opacity-100"
      >
        {text}
      </span>
    </span>
  )
}
