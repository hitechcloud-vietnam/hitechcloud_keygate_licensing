import { RefreshCw, WifiOff } from "lucide-react"
import { useState } from "react"
import { Button } from "@/components/ui/button"
import { useSiteConfig } from "@/hooks/use-site-config"
import { useI18n } from "@/i18n"

// Shown while the session cannot be confirmed because the server is out
// of reach. The user may well still be signed in: no login redirect, and
// AuthProvider keeps retrying in the background.
export function ServiceUnavailableScreen({ onRetry }: { onRetry: () => Promise<void> }) {
  const { t } = useI18n()
  // Falls back to the built-in attribution when the config request
  // fails too, which it will while the server is out of reach.
  const { attribution_text, attribution_url } = useSiteConfig()
  const [retrying, setRetrying] = useState(false)
  return (
    <div className="flex h-screen flex-col items-center justify-center px-4">
      <div role="alert" className="max-w-sm space-y-4 text-center">
        <WifiOff className="mx-auto h-8 w-8 text-muted-foreground" />
        <h1 className="text-lg font-semibold">{t("common.unavailableTitle")}</h1>
        <p className="text-sm text-muted-foreground">{t("common.unavailableBody")}</p>
        <Button
          variant="outline"
          disabled={retrying}
          onClick={async () => {
            setRetrying(true)
            try {
              await onRetry()
            } finally {
              setRetrying(false)
            }
          }}
        >
          <RefreshCw className={`mr-2 h-4 w-4 ${retrying ? "animate-spin" : ""}`} />
          {t("common.retry")}
        </Button>
      </div>
      {/* Attribution required by AGPL v3 Section 7(b) — see NOTICE */}
      <a
        href={attribution_url}
        target="_blank"
        rel="noopener noreferrer"
        className="mt-8 text-[10px] text-muted-foreground/40 hover:text-muted-foreground transition-colors"
      >
        {attribution_text}
      </a>
    </div>
  )
}
