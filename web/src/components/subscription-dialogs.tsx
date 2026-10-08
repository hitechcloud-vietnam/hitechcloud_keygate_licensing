import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { useState } from "react"
import { showToast, toastError } from "@/components/toast"
import {
  AlertDialog,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import type { TranslationKeys } from "@/i18n"
import { useI18n } from "@/i18n"
import type { Invoice, Plan, PortalLicense } from "@/lib/api"
import { portal } from "@/lib/api"
import { formatMinor } from "@/lib/money"
import { formatDate } from "@/lib/utils"

// Shared subscription management dialogs used by both the Licenses card
// and the Subscriptions page. Keeping one implementation means the
// cancel / change-plan / invoices flows behave identically wherever a
// customer reaches them.

export function CancelDialog({
  licenseId,
  provider,
  productName,
  onClose,
}: {
  licenseId: string
  provider: string
  productName: string
  onClose: () => void
}) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [immediate, setImmediate] = useState(false)

  const cancelMut = useMutation({
    mutationFn: () => portal.cancelSubscription({ license_id: licenseId, immediate }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "licenses"] })
      onClose()
    },
    onError: (e: Error) => toastError(e),
  })

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("portal.cancelSubscription")}</DialogTitle>
          <DialogDescription>{t("portal.cancelDesc", { product: productName })}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          <div className="space-y-4">
            {provider === "stripe" && (
              <label className="flex items-center gap-2 text-sm">
                <input
                  type="checkbox"
                  checked={immediate}
                  onChange={(e) => setImmediate(e.target.checked)}
                  className="rounded"
                />
                {t("portal.cancelImmediately")}
              </label>
            )}
            <p className="text-sm text-muted-foreground">
              {immediate ? t("portal.cancelImmediateWarning") : t("portal.cancelEndWarning")}
            </p>
            <div className="flex justify-end gap-2">
              <Button variant="outline" onClick={onClose}>
                {t("common.cancel")}
              </Button>
              <Button variant="destructive" onClick={() => cancelMut.mutate()} disabled={cancelMut.isPending}>
                {cancelMut.isPending ? t("common.loading") : t("portal.confirmCancel")}
              </Button>
            </div>
          </div>
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}

export function InvoicesDialog({ licenseId, onClose }: { licenseId: string; onClose: () => void }) {
  const { t } = useI18n()
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "invoices", licenseId],
    queryFn: () => portal.getInvoices(licenseId),
  })
  const invoices = data?.invoices || []

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-2xl h-[min(640px,85vh)]">
        <DialogHeader>
          <DialogTitle>{t("portal.invoices")}</DialogTitle>
        </DialogHeader>
        <DialogBody>
          {isLoading ? (
            <div className="h-32 animate-pulse bg-muted rounded-lg" />
          ) : invoices.length === 0 ? (
            <p className="text-sm text-muted-foreground text-center py-8">{t("common.noData")}</p>
          ) : (
            <div className="space-y-2">
              {invoices.map((inv: Invoice) => (
                <div
                  key={inv.id}
                  className="flex items-center justify-between bg-muted/50 rounded-lg px-4 py-3 text-sm"
                >
                  <div>
                    <p className="font-medium">{inv.number || inv.id}</p>
                    {/* Stripe timestamps are Unix seconds; formatDate wants a Date. */}
                    <p className="text-xs text-muted-foreground">{formatDate(new Date(inv.created * 1000))}</p>
                  </div>
                  <div className="flex items-center gap-3">
                    <Badge
                      className={
                        inv.status === "paid" ? "bg-emerald-100 text-emerald-800" : "bg-amber-100 text-amber-800"
                      }
                    >
                      {t(`status.${inv.status}` as TranslationKeys)}
                    </Badge>
                    <span className="font-medium">{formatMinor(inv.amount_paid, inv.currency)}</span>
                    {inv.invoice_pdf && (
                      <Button variant="ghost" size="sm" asChild>
                        <a href={inv.invoice_pdf} target="_blank" rel="noopener noreferrer">
                          PDF
                        </a>
                      </Button>
                    )}
                  </div>
                </div>
              ))}
            </div>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}

export function ChangePlanDialog({ license, onClose }: { license: PortalLicense; onClose: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()

  const { data: plansData } = useQuery({
    queryKey: ["portal", "plans", license.product_id],
    queryFn: () => portal.listPlans(license.product_id),
  })
  // Only other subscription plans with a Stripe price: a subscription
  // can move between prices, not become a one-time purchase. Mirrors the
  // backend's change-plan gate.
  const plans = (plansData?.plans || []).filter(
    (p: Plan) => p.id !== license.plan_id && p.license_type === "subscription" && p.stripe_price_id,
  )

  const [confirming, setConfirming] = useState<Plan | null>(null)
  const changeMut = useMutation({
    mutationFn: (plan: Plan) => portal.changePlan({ license_id: license.id, new_price_id: plan.stripe_price_id || "" }),
    onSuccess: (_, plan) => {
      qc.invalidateQueries({ queryKey: ["portal", "licenses"] })
      showToast(t("portal.planChanged", { plan: plan.name }), "success")
      onClose()
    },
    onError: (e: Error) => {
      setConfirming(null)
      toastError(e)
    },
  })

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="h-[min(560px,85vh)]">
        <DialogHeader>
          <DialogTitle>{t("portal.changePlan")}</DialogTitle>
          <DialogDescription>{t("portal.changePlanDesc")}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          {plans.length === 0 ? (
            <p className="text-sm text-muted-foreground text-center py-4">{t("portal.noOtherPlans")}</p>
          ) : (
            <div className="space-y-2">
              {plans.map((plan: Plan) => (
                <div key={plan.id} className="flex items-center justify-between bg-muted/50 rounded-lg px-4 py-3">
                  <div>
                    <p className="font-medium text-sm">{plan.name}</p>
                    <p className="text-xs text-muted-foreground">
                      {plan.license_type}
                      {plan.billing_interval ? ` · ${plan.billing_interval}` : ""}
                    </p>
                  </div>
                  <Button size="sm" onClick={() => setConfirming(plan)} disabled={changeMut.isPending}>
                    {t("portal.switchTo")}
                  </Button>
                </div>
              ))}
            </div>
          )}
        </DialogBody>
        <AlertDialog
          open={confirming !== null}
          onOpenChange={(open) => !open && !changeMut.isPending && setConfirming(null)}
        >
          <AlertDialogContent>
            <AlertDialogHeader>
              <AlertDialogTitle>{t("portal.confirmSwitchTitle", { plan: confirming?.name ?? "" })}</AlertDialogTitle>
              <AlertDialogDescription>{t("portal.confirmSwitchDesc")}</AlertDialogDescription>
            </AlertDialogHeader>
            <div className="flex justify-end gap-2">
              <AlertDialogCancel disabled={changeMut.isPending}>{t("common.cancel")}</AlertDialogCancel>
              <Button onClick={() => confirming && changeMut.mutate(confirming)} disabled={changeMut.isPending}>
                {changeMut.isPending ? t("common.loading") : t("portal.confirmSwitch")}
              </Button>
            </div>
          </AlertDialogContent>
        </AlertDialog>
      </DialogContent>
    </Dialog>
  )
}
