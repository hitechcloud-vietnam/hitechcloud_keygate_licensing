import { useQuery } from "@tanstack/react-query"
import { AlertCircle, CheckCircle2, Clock, Home, Loader2, XCircle } from "lucide-react"
import { Link, useLocation } from "react-router-dom"
import { LanguageSwitcher } from "@/components/language-switcher"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { useSiteConfig } from "@/hooks/use-site-config"
import { useI18n } from "@/i18n"
import { ApiError, checkout } from "@/lib/api"

// The gateway return page: after Pay2S / ZaloPay / payOS collects the
// money, the buyer's browser lands here with ?order=<order_number>.
// The order is settled server-side by the gateway's IPN — this page
// only polls GET /checkout/gateway-pay/status until that lands (or
// until the gateway admits failure). Never trust the query string for
// anything but WHICH order to ask about.
export default function GatewayReturnPage() {
  const { t } = useI18n()
  const { site_name, logo_url, attribution_text, attribution_url } = useSiteConfig()
  const { search } = useLocation()
  const orderNumber = new URLSearchParams(search).get("order") || ""

  // Poll every 3s for up to 2 minutes. IPNs usually land in seconds;
  // Pay2S retries on a 5min/15min/1h/24h ladder, so a still-pending
  // answer after the budget is honest — the licence email will follow.
  const status = useQuery({
    queryKey: ["gateway-pay-status", orderNumber],
    enabled: !!orderNumber,
    retry: true,
    retryDelay: 3000,
    refetchInterval: (query) => {
      const s = query.state.data?.status
      return s === "succeeded" || s === "failed" || s === "cancelled" || s === "expired" ? false : 3000
    },
    staleTime: 0,
    queryFn: () => checkout.gatewayPayStatus(orderNumber),
  })

  const notFound = status.error instanceof ApiError && status.error.status === 404
  const current = status.data?.status
  const settled = current === "succeeded"
  const failed = current === "failed" || current === "cancelled" || current === "expired" || current === "refunded"

  return (
    <div className="min-h-screen bg-background flex flex-col">
      <header className="border-b bg-card">
        <div className="max-w-3xl mx-auto flex items-center justify-between gap-2 h-14 px-4">
          <div className="flex items-center gap-2">
            <img src={logo_url || "/logo.svg"} alt={site_name} className="h-6 w-6" />
            <span className="font-bold text-lg tracking-tight">{site_name}</span>
          </div>
          <LanguageSwitcher />
        </div>
      </header>

      <main className="flex-1 max-w-3xl w-full mx-auto p-4 md:p-8">
        <div className="space-y-6">
          <div>
            <h1 className="text-2xl font-bold tracking-tight">{t("gatewayReturn.title")}</h1>
          </div>

          <Card>
            <CardContent className="py-12 text-center space-y-4">
              {!orderNumber || notFound ? (
                <div className="space-y-3 text-muted-foreground">
                  <AlertCircle className="h-10 w-10 mx-auto text-destructive" />
                  <p>{t("gatewayReturn.notFound")}</p>
                </div>
              ) : settled ? (
                <div className="space-y-3">
                  <CheckCircle2 className="h-10 w-10 mx-auto text-emerald-600" />
                  <p className="text-lg font-medium">{t("gatewayReturn.success")}</p>
                  <p className="text-sm text-muted-foreground">
                    {t("gatewayReturn.successBody", { order: orderNumber })}
                  </p>
                </div>
              ) : failed ? (
                <div className="space-y-3">
                  <XCircle className="h-10 w-10 mx-auto text-destructive" />
                  <p className="text-lg font-medium">{t("gatewayReturn.failed")}</p>
                  <p className="text-sm text-muted-foreground">
                    {t("gatewayReturn.failedBody", { order: orderNumber })}
                  </p>
                </div>
              ) : status.isError ? (
                <div className="space-y-3">
                  <AlertCircle className="h-10 w-10 mx-auto text-destructive" />
                  <p className="text-sm text-muted-foreground">{t("checkout.loadError")}</p>
                  <Button variant="outline" onClick={() => status.refetch()}>
                    {t("common.retry")}
                  </Button>
                </div>
              ) : current === "pending" ? (
                <div className="space-y-3">
                  <Clock className="h-10 w-10 mx-auto text-amber-500" />
                  <p className="text-lg font-medium">{t("gatewayReturn.pending")}</p>
                  <p className="text-sm text-muted-foreground">
                    {t("gatewayReturn.pendingBody", { order: orderNumber })}
                  </p>
                </div>
              ) : (
                <div className="space-y-3">
                  <Loader2 className="h-10 w-10 mx-auto animate-spin text-muted-foreground" />
                  <p className="text-lg font-medium">{t("gatewayReturn.processing")}</p>
                  <p className="text-sm text-muted-foreground">{t("gatewayReturn.processingHint")}</p>
                </div>
              )}

              {orderNumber && !notFound && (
                <p className="text-xs text-muted-foreground">
                  {t("gatewayReturn.orderLabel")}: <span className="font-mono">{orderNumber}</span>
                </p>
              )}

              <div className="pt-2">
                <Button variant="outline" asChild>
                  <Link to="/">
                    <Home className="h-4 w-4 mr-2" /> {t("gatewayReturn.backHome")}
                  </Link>
                </Button>
              </div>
            </CardContent>
          </Card>
        </div>
      </main>

      {/* Attribution required by AGPL v3 Section 7(b) — see NOTICE */}
      <footer className="border-t py-3 text-center">
        <a
          href={attribution_url}
          target="_blank"
          rel="noopener noreferrer"
          className="text-xs text-muted-foreground/50 hover:text-muted-foreground transition-colors"
        >
          {attribution_text}
        </a>
      </footer>
    </div>
  )
}
