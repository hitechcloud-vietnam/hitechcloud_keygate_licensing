import { useQuery, useQueryClient } from "@tanstack/react-query"
import { CreditCard, Receipt, RefreshCw } from "lucide-react"
import { useState } from "react"
import { CancelDialog, ChangePlanDialog, InvoicesDialog } from "@/components/subscription-dialogs"
import { toastError } from "@/components/toast"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Separator } from "@/components/ui/separator"
import { useAuth } from "@/hooks/use-auth"
import { type TranslationKeys, useI18n } from "@/i18n"
import type { PortalLicense } from "@/lib/api"
import { portal } from "@/lib/api"
import { formatDate, statusColor } from "@/lib/utils"

// The Subscriptions surface collects everything a customer can act on
// billing-wise: Stripe subscriptions (change plan / cancel / update
// payment method / invoices) and perpetual licences' update-maintenance
// renewals ("extend updates"). A licence shows here when it is
// subscription-backed or perpetual — the two kinds that carry ongoing
// billing or renewal.
function isManageable(lic: PortalLicense): boolean {
  return !!lic.stripe_subscription_id || lic.plan?.license_type === "perpetual"
}

export default function PortalSubscriptionsPage() {
  const { t } = useI18n()
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "licenses"],
    queryFn: portal.licenses,
  })

  const licenses = (data?.licenses || []).filter(isManageable)

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("portal.subsTitle")}</h1>
        <p className="text-muted-foreground">{t("portal.subsDesc")}</p>
      </div>

      {isLoading ? (
        <div className="space-y-4">
          {[1, 2].map((i) => (
            <div key={i} className="h-40 animate-pulse bg-muted rounded-lg" />
          ))}
        </div>
      ) : licenses.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center">
            <CreditCard className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
            <p className="text-lg font-medium">{t("portal.subsEmpty")}</p>
            <p className="text-muted-foreground mt-1">{t("portal.subsEmptyDesc")}</p>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-4">
          {licenses.map((lic) => (
            <SubscriptionCard key={lic.id} license={lic} />
          ))}
        </div>
      )}
    </div>
  )
}

function SubscriptionCard({ license: lic }: { license: PortalLicense }) {
  const { t } = useI18n()
  const { user } = useAuth()
  const qc = useQueryClient()
  // Billing actions belong to the licence holder; a seat may see the
  // licence but cannot change how it is paid for. The server authorises
  // these by the licence's own address — mirror that so the buttons
  // never lead to a 403.
  const isOwner = !!user?.email && user.email.toLowerCase() === lic.email.toLowerCase()

  const [showInvoices, setShowInvoices] = useState(false)
  const [showChangePlan, setShowChangePlan] = useState(false)
  const [showCancel, setShowCancel] = useState(false)
  const [renewing, setRenewing] = useState(false)

  const hasSubscription = lic.payment_provider === "stripe" && !!lic.stripe_subscription_id
  const isPerpetualPlan = lic.plan?.license_type === "perpetual"
  const isSubscriptionPlan = lic.plan?.license_type === "subscription"

  // Perpetual maintenance: updates_until bounds which releases may be
  // installed. The renewal flow needs a live Stripe renewal price and a
  // renewal length on the plan, and only applies to an active licence.
  const updatesUntil = lic.updates_until ? new Date(lic.updates_until) : null
  const updatesEnded = updatesUntil !== null && updatesUntil.getTime() < Date.now()
  const canRenewUpdates =
    isOwner &&
    isPerpetualPlan &&
    updatesUntil !== null &&
    lic.status === "active" &&
    lic.plan?.active !== false &&
    (lic.plan?.renewal_days ?? 0) > 0 &&
    !!lic.plan?.stripe_renewal_price_id

  const handleBillingPortal = async () => {
    try {
      const res = await portal.getBillingPortal({ license_id: lic.id })
      window.location.href = res.url
    } catch (err) {
      toastError(err)
    }
  }

  const handleRenewUpdates = async () => {
    setRenewing(true)
    try {
      const res = await portal.renewUpdates({ license_id: lic.id })
      window.location.href = res.url
    } catch (err) {
      toastError(err)
      setRenewing(false)
    }
  }

  const handleCancelScheduled = () => {
    qc.invalidateQueries({ queryKey: ["portal", "licenses"] })
  }

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between gap-3 flex-wrap">
          <div>
            <CardTitle className="text-lg">{lic.product?.name || t("common.product")}</CardTitle>
            <p className="text-sm text-muted-foreground mt-1">{lic.plan?.name}</p>
          </div>
          <div className="flex items-center gap-2">
            <Badge variant="secondary">
              {isSubscriptionPlan ? t("portal.subsSubscription") : t("portal.subsPerpetual")}
            </Badge>
            <Badge className={statusColor(lic.status)}>{t(`status.${lic.status}` as TranslationKeys)}</Badge>
          </div>
        </div>
      </CardHeader>
      <CardContent className="space-y-4">
        {/* Billing facts */}
        <div className="grid grid-cols-2 md:grid-cols-4 gap-4 text-sm">
          {isSubscriptionPlan && lic.plan?.billing_interval && (
            <div>
              <p className="text-muted-foreground">{t("portal.subsBillingInterval")}</p>
              <p className="font-medium capitalize">{lic.plan.billing_interval}</p>
            </div>
          )}
          <div>
            <p className="text-muted-foreground">
              {isPerpetualPlan ? t("portal.updatesUntil") : t("portal.subsCurrentPeriodEnd")}
            </p>
            <p className="font-medium">
              {isPerpetualPlan
                ? lic.updates_until
                  ? formatDate(lic.updates_until)
                  : t("portal.updatesForLife")
                : formatDate(lic.valid_until)}
            </p>
          </div>
          <div>
            <p className="text-muted-foreground">{t("common.status")}</p>
            <p className="font-medium">{t(`status.${lic.status}` as TranslationKeys)}</p>
          </div>
          <div>
            <p className="text-muted-foreground">{t("common.email")}</p>
            <p className="font-medium truncate">{lic.email}</p>
          </div>
        </div>

        {isPerpetualPlan && updatesEnded && (
          <p className="text-sm text-amber-600">{t("portal.updatesEnded", { date: formatDate(lic.updates_until) })}</p>
        )}

        <Separator />

        {/* Actions */}
        <div className="flex gap-2 flex-wrap">
          {lic.payment_provider === "stripe" && (
            <Button variant="outline" size="sm" onClick={() => setShowInvoices(true)}>
              <Receipt className="h-4 w-4 mr-1.5" /> {t("portal.viewInvoices")}
            </Button>
          )}
          {isOwner && hasSubscription && (
            <>
              <Button variant="outline" size="sm" onClick={handleBillingPortal}>
                {t("portal.updatePayment")}
              </Button>
              {(lic.status === "active" || lic.status === "trialing") && (
                <>
                  <Button variant="outline" size="sm" onClick={() => setShowChangePlan(true)}>
                    {t("portal.changePlan")}
                  </Button>
                  <Button variant="outline" size="sm" className="text-destructive" onClick={() => setShowCancel(true)}>
                    {t("portal.cancelSubscription")}
                  </Button>
                </>
              )}
            </>
          )}
          {canRenewUpdates && (
            <Button size="sm" onClick={handleRenewUpdates} disabled={renewing}>
              <RefreshCw className="h-4 w-4 mr-1.5" /> {renewing ? t("common.loading") : t("portal.renewUpdates")}
            </Button>
          )}
        </div>
      </CardContent>

      {showCancel && (
        <CancelDialog
          licenseId={lic.id}
          provider={lic.payment_provider || ""}
          productName={lic.product?.name || ""}
          onClose={() => {
            setShowCancel(false)
            handleCancelScheduled()
          }}
        />
      )}
      {showInvoices && <InvoicesDialog licenseId={lic.id} onClose={() => setShowInvoices(false)} />}
      {showChangePlan && <ChangePlanDialog license={lic} onClose={() => setShowChangePlan(false)} />}
    </Card>
  )
}
