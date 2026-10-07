import { Check, Copy } from "lucide-react"
import { useState } from "react"
import { showToast } from "@/components/toast"
import { Button } from "@/components/ui/button"
import { useI18n } from "@/i18n"

// Shows a record's ID with a copy button. The API addresses products,
// plans and licenses by ID, so integrators need it without opening the
// database.
export function CopyableId({ id }: { id: string }) {
  const { t } = useI18n()
  const [copied, setCopied] = useState(false)
  const copy = async () => {
    try {
      await navigator.clipboard.writeText(id)
      setCopied(true)
      setTimeout(() => setCopied(false), 2000)
    } catch {
      // No clipboard over plain HTTP on a LAN address; the ID text is
      // selectable, so say so.
      showToast(t("common.copyFailed"), "error")
    }
  }
  return (
    <div className="flex min-w-0 items-center gap-1.5 text-xs text-muted-foreground">
      <span className="shrink-0">ID</span>
      <code className="truncate rounded bg-muted px-1.5 py-0.5 font-mono select-all" title={id}>
        {id}
      </code>
      <Button
        type="button"
        variant="ghost"
        size="icon"
        className="h-6 w-6 shrink-0"
        title={t("common.copyId")}
        aria-label={t("common.copyId")}
        onClick={copy}
      >
        {copied ? <Check className="h-3 w-3 text-emerald-600" /> : <Copy className="h-3 w-3" />}
      </Button>
    </div>
  )
}
