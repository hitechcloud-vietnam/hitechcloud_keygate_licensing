import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, RotateCcw } from "lucide-react"
import { useState } from "react"
import { Link, useParams } from "react-router-dom"
import { CopyableId } from "@/components/copyable-id"
import { showToast } from "@/components/toast"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableEmpty,
  DataTableHead,
  DataTableHeader,
  DataTableRow,
} from "@/components/ui/data-table"
import { Separator } from "@/components/ui/separator"
import { type TranslationKeys, useI18n } from "@/i18n"
import type { OrderInvoice } from "@/lib/api"
import { admin } from "@/lib/api"
import { formatBps, formatMinor } from "@/lib/money"
import { formatDate } from "@/lib/utils"
import { statusBadgeColor } from "@/pages/admin/orders"

export default function OrderDetailPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const { id = "" } = useParams()
  const [refunding, setRefunding] = useState(false)

  const { data, isLoading, error } = useQuery({
    queryKey: ["admin", "orders", id],
    queryFn: () => admin.getOrder(id),
    enabled: !!id,
  })
  // The order payload carries its invoices too, but the ledger has a
  // dedicated listing for them — that is the one this page shows, so
  // what is on screen is what the endpoint says.
  const { data: invoicesData } = useQuery({
    queryKey: ["admin", "orders", id, "invoices"],
    queryFn: () => admin.listOrderInvoices(id),
    enabled: !!id,
  })

  const refundMut = useMutation({
    mutationFn: () => admin.refundOrder(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "orders"] })
      showToast(t("toast.orderRefunded"), "success")
      setRefunding(false)
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  if (isLoading) {
    return (
      <div className="space-y-6">
        <div className="h-8 w-64 animate-pulse bg-muted rounded-lg" />
        <div className="h-64 animate-pulse bg-muted rounded-lg" />
      </div>
    )
  }

  const order = data?.order
  if (!order) {
    return (
      <div className="space-y-6">
        <Link
          to="/admin/orders"
          className="inline-flex items-center gap-2 text-sm text-muted-foreground hover:underline"
        >
          <ArrowLeft className="h-4 w-4" /> {t("orders.backToList")}
        </Link>
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            {error ? (error instanceof Error ? error.message : String(error)) : t("orders.notFound")}
          </CardContent>
        </Card>
      </div>
    )
  }

  const invoices = invoicesData?.invoices || []
  const items = order.items || []

  return (
    <div className="space-y-6">
      <Link to="/admin/orders" className="inline-flex items-center gap-2 text-sm text-muted-foreground hover:underline">
        <ArrowLeft className="h-4 w-4" /> {t("orders.backToList")}
      </Link>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-3">
            <h1 className="text-2xl font-bold tracking-tight">{order.order_number}</h1>
            <Badge className={statusBadgeColor(order.status)}>{t(`status.${order.status}` as TranslationKeys)}</Badge>
          </div>
          <p className="text-muted-foreground">
            {order.customer_email}
            {order.customer_name ? ` · ${order.customer_name}` : ""}
          </p>
        </div>
        {/* Only money that came in can go back out: the button appears
            for a paid order alone, and the server refuses the rest. */}
        {order.status === "paid" && (
          <Button variant="destructive" onClick={() => setRefunding(true)}>
            <RotateCcw className="h-4 w-4 mr-2" /> {t("orders.refund")}
          </Button>
        )}
      </div>

      <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
        <div className="lg:col-span-2 space-y-6">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">{t("orders.items")}</CardTitle>
            </CardHeader>
            <CardContent>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableHead>{t("orders.colDescription")}</DataTableHead>
                    <DataTableHead>{t("orders.colQty")}</DataTableHead>
                    <DataTableHead>{t("orders.colUnitPrice")}</DataTableHead>
                    <DataTableHead>{t("orders.colLineSubtotal")}</DataTableHead>
                    <DataTableHead>{t("orders.colLineDiscount")}</DataTableHead>
                    <DataTableHead>{t("orders.colLineTax")}</DataTableHead>
                    <DataTableHead>{t("orders.colLineTotal")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {items.length === 0 && <DataTableEmpty colSpan={7} message={t("orders.itemsEmpty")} />}
                  {items.map((it) => (
                    <DataTableRow key={it.id}>
                      <DataTableCell className="font-medium">
                        {it.description || it.sku || it.product_id || "-"}
                      </DataTableCell>
                      <DataTableCell>{it.quantity}</DataTableCell>
                      <DataTableCell>{formatMinor(it.unit_amount_minor, order.currency)}</DataTableCell>
                      <DataTableCell>{formatMinor(it.line_subtotal_minor, order.currency)}</DataTableCell>
                      <DataTableCell>{formatMinor(it.line_discount_minor, order.currency)}</DataTableCell>
                      <DataTableCell>{formatMinor(it.line_tax_minor, order.currency)}</DataTableCell>
                      <DataTableCell className="font-medium">
                        {formatMinor(it.line_total_minor, order.currency)}
                      </DataTableCell>
                    </DataTableRow>
                  ))}
                </DataTableBody>
              </DataTable>
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">{t("orders.summary")}</CardTitle>
            </CardHeader>
            <CardContent className="space-y-2 text-sm">
              <div className="flex justify-between">
                <span className="text-muted-foreground">{t("orders.subtotal")}</span>
                <span>{formatMinor(order.subtotal_minor, order.currency)}</span>
              </div>
              <div className="flex justify-between">
                <span className="text-muted-foreground">{t("orders.discount")}</span>
                <span>{formatMinor(order.discount_minor, order.currency)}</span>
              </div>
              {order.coupon_code && (
                <div className="flex justify-between text-xs text-muted-foreground">
                  <span>
                    {t("orders.coupon")}: {order.coupon_code} ({couponTypeLabel(t, order.coupon_type || "")})
                  </span>
                  <span>
                    {order.coupon_type === "percent_off"
                      ? formatBps(order.coupon_value_bps || 0)
                      : formatMinor(order.coupon_value_minor || 0, order.currency)}
                  </span>
                </div>
              )}
              <div className="flex justify-between">
                <span className="text-muted-foreground">{t("orders.tax")}</span>
                <span>{formatMinor(order.tax_minor, order.currency)}</span>
              </div>
              <div className="flex justify-between text-xs text-muted-foreground">
                <span>
                  {t("orders.taxJurisdictions")}: {order.tax_jurisdiction || "-"} (
                  {formatBps(order.tax_basis_points || 0)})
                </span>
                <span>{order.tax_inclusive ? t("orders.taxInclusive") : t("orders.taxExclusive")}</span>
              </div>
              <Separator />
              <div className="flex justify-between font-medium text-base">
                <span>{t("orders.colTotal")}</span>
                <span>{formatMinor(order.total_minor, order.currency)}</span>
              </div>
            </CardContent>
          </Card>
        </div>

        <div className="space-y-6">
          <Card>
            <CardHeader>
              <CardTitle className="text-base">{t("orders.payment")}</CardTitle>
            </CardHeader>
            <CardContent className="space-y-3 text-sm">
              <DetailRow label={t("common.created")} value={formatDate(order.created_at)} />
              <DetailRow label={t("orders.paidAt")} value={formatDate(order.paid_at)} />
              <DetailRow label={t("orders.refundedAt")} value={formatDate(order.refunded_at)} />
              <DetailRow label={t("orders.provider")} value={order.payment_provider || "-"} />
              {order.external_id && (
                <div className="space-y-1">
                  <p className="text-xs text-muted-foreground">{t("orders.externalId")}</p>
                  <CopyableId id={order.external_id} />
                </div>
              )}
              {order.license_id && (
                <div className="space-y-1">
                  <div className="flex items-center justify-between gap-2">
                    <p className="text-xs text-muted-foreground">{t("orders.license")}</p>
                    <Link to={`/admin/licenses?id=${order.license_id}`} className="text-xs hover:underline">
                      {t("orders.viewDetail")}
                    </Link>
                  </div>
                  <CopyableId id={order.license_id} />
                </div>
              )}
            </CardContent>
          </Card>

          <Card>
            <CardHeader>
              <CardTitle className="text-base">{t("orders.invoices")}</CardTitle>
            </CardHeader>
            <CardContent>
              {invoices.length === 0 ? (
                <p className="text-sm text-muted-foreground">{t("orders.invoicesEmpty")}</p>
              ) : (
                <div className="space-y-3">
                  {invoices.map((inv: OrderInvoice) => (
                    <div key={inv.id} className="rounded-lg border p-3 space-y-2">
                      <div className="flex items-center justify-between gap-2">
                        <span className="font-medium text-sm">{inv.invoice_number}</span>
                        <Badge
                          className={statusBadgeColor(
                            inv.status === "paid" ? "paid" : inv.status === "void" ? "failed" : "pending",
                          )}
                        >
                          {t(`status.${inv.status}` as TranslationKeys)}
                        </Badge>
                      </div>
                      <div className="flex justify-between text-sm">
                        <span className="text-muted-foreground">{t("orders.colTotal")}</span>
                        <span className="font-medium">{formatMinor(inv.total_minor, inv.currency)}</span>
                      </div>
                      <div className="grid grid-cols-3 gap-2 text-xs text-muted-foreground">
                        <span>
                          {t("orders.issuedAt")}: {formatDate(inv.issued_at)}
                        </span>
                        <span>
                          {t("orders.dueAt")}: {formatDate(inv.due_at)}
                        </span>
                        <span>
                          {t("orders.paidAt")}: {formatDate(inv.paid_at)}
                        </span>
                      </div>
                    </div>
                  ))}
                </div>
              )}
            </CardContent>
          </Card>
        </div>
      </div>

      <AlertDialog open={refunding} onOpenChange={() => setRefunding(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("orders.refundTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("orders.refundDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => refundMut.mutate()}
            >
              {t("orders.refund")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function DetailRow({ label, value }: { label: string; value: string }) {
  return (
    <div className="flex justify-between gap-2">
      <span className="text-muted-foreground">{label}</span>
      <span>{value}</span>
    </div>
  )
}

// An order recorded a coupon through the pricing engine, which spells
// the fixed type "fixed_amount_off" while the stored coupon rows spell
// it "fixed_off". Both read as the same thing to the admin.
function couponTypeLabel(t: (key: TranslationKeys) => string, type: string): string {
  if (type === "percent_off") return t("coupons.percentOff")
  if (type === "fixed_off" || type === "fixed_amount_off") return t("coupons.fixedOff")
  return type
}
