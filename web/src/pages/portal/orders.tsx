import { useQuery } from "@tanstack/react-query"
import { ChevronDown, ChevronRight, Receipt, ShoppingCart } from "lucide-react"
import { useState } from "react"
import { ListEmptyState } from "@/components/empty-state"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeader,
  DataTablePagination,
  DataTableRow,
  useServerPagination,
} from "@/components/ui/data-table"
import { Separator } from "@/components/ui/separator"
import { type TranslationKeys, useI18n } from "@/i18n"
import type { Order, OrderInvoice } from "@/lib/api"
import { portal } from "@/lib/api"
import { formatBps, formatMinor } from "@/lib/money"
import { formatDate } from "@/lib/utils"
import { statusBadgeColor } from "@/pages/admin/orders"

const ORDER_STATUSES = ["pending", "paid", "failed", "refunded"] as const

export default function PortalOrdersPage() {
  const { t } = useI18n()
  const [statusFilter, setStatusFilter] = useState<string>("")
  const [expandedId, setExpandedId] = useState<string | null>(null)
  const pg = useServerPagination(10, [statusFilter])
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "orders", statusFilter, pg.page, pg.pageSize],
    queryFn: () => portal.listPortalOrders({ status: statusFilter || undefined, ...pg.params }),
  })

  const { items: orders, total, totalPages } = pg.from(data, data?.orders)

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("portal.ordersTitle")}</h1>
        <p className="text-muted-foreground">{t("portal.ordersDesc")}</p>
      </div>

      <div className="flex flex-wrap gap-3">
        <div className="flex flex-wrap gap-2">
          <StatusChip
            active={statusFilter === ""}
            label={t("filter.allStatuses")}
            onClick={() => setStatusFilter("")}
          />
          {ORDER_STATUSES.map((s) => (
            <StatusChip
              key={s}
              active={statusFilter === s}
              label={t(`status.${s}` as TranslationKeys)}
              onClick={() => setStatusFilter(s)}
            />
          ))}
        </div>
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-32 animate-pulse bg-muted rounded-lg" />
          ) : orders.length === 0 ? (
            // A status chip can narrow the ledger to nothing — that is
            // a filter story (clear it), not an empty-accounts story.
            <ListEmptyState
              icon={ShoppingCart}
              title={t("portal.ordersEmpty")}
              description={t("empty.orders.customerDesc")}
              action={{ label: t("empty.browseMarketplace"), to: "/marketplace" }}
              filtered={statusFilter !== ""}
              filteredTitle={t("filter.noMatches")}
              filteredDescription={t("empty.filteredDesc")}
              clearLabel={t("common.clearFilters")}
              onClearFilters={() => setStatusFilter("")}
            />
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableHead>{t("orders.orderNumber")}</DataTableHead>
                    <DataTableHead>{t("common.status")}</DataTableHead>
                    <DataTableHead>{t("orders.colTotal")}</DataTableHead>
                    <DataTableHead>{t("orders.coupon")}</DataTableHead>
                    <DataTableHead>{t("common.created")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {orders.map((o: Order) => (
                    <OrderRows
                      key={o.id}
                      order={o}
                      expanded={expandedId === o.id}
                      onToggle={() => setExpandedId((cur) => (cur === o.id ? null : o.id))}
                    />
                  ))}
                </DataTableBody>
              </DataTable>
              {total > 0 && (
                <DataTablePagination
                  page={pg.page}
                  totalPages={totalPages}
                  total={total}
                  pageSize={pg.pageSize}
                  onPageChange={pg.setPage}
                  onPageSizeChange={pg.setPageSize}
                />
              )}
            </>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

// The status chips narrow the list server-side. They are the filter the
// page asks the ledger for, not a client-side slice of one page.
function StatusChip({ active, label, onClick }: { active: boolean; label: string; onClick: () => void }) {
  return (
    <button
      type="button"
      onClick={onClick}
      className={
        active
          ? "rounded-full border border-primary bg-primary/10 px-3 py-1 text-sm font-medium text-primary"
          : "rounded-full border border-transparent bg-muted px-3 py-1 text-sm text-muted-foreground hover:text-foreground"
      }
    >
      {label}
    </button>
  )
}

// One order row, plus (when open) the detail row beneath it. Toggling
// the row expands the order's items, summary and invoices in place.
function OrderRows({ order, expanded, onToggle }: { order: Order; expanded: boolean; onToggle: () => void }) {
  const { t } = useI18n()
  return (
    <>
      <DataTableRow onClick={onToggle} className="cursor-pointer">
        <DataTableCell>
          <span className="inline-flex items-center gap-2 font-medium">
            {expanded ? (
              <ChevronDown className="h-4 w-4 text-muted-foreground" />
            ) : (
              <ChevronRight className="h-4 w-4 text-muted-foreground" />
            )}
            {order.order_number}
          </span>
        </DataTableCell>
        <DataTableCell>
          <Badge className={statusBadgeColor(order.status)}>{t(`status.${order.status}` as TranslationKeys)}</Badge>
        </DataTableCell>
        <DataTableCell className="font-medium">{formatMinor(order.total_minor, order.currency)}</DataTableCell>
        <DataTableCell className="text-muted-foreground">{order.coupon_code || "-"}</DataTableCell>
        <DataTableCell className="text-muted-foreground">{formatDate(order.created_at)}</DataTableCell>
      </DataTableRow>
      {expanded && (
        <DataTableRow>
          <DataTableCell colSpan={5} className="bg-muted/20 px-4 py-4">
            <OrderDetail orderId={order.id} />
          </DataTableCell>
        </DataTableRow>
      )}
    </>
  )
}

// The expanded detail: the order with its line items and invoices, read
// from GET /portal/orders/:id. Everything is integer minor units, shown
// through the shared money helpers.
function OrderDetail({ orderId }: { orderId: string }) {
  const { t } = useI18n()
  const { data, isLoading, error } = useQuery({
    queryKey: ["portal", "order", orderId],
    queryFn: () => portal.getPortalOrder(orderId),
    enabled: !!orderId,
  })

  if (isLoading) return <div className="h-24 animate-pulse bg-muted rounded-lg" />
  if (error || !data?.order) {
    return <p className="text-sm text-muted-foreground">{t("portal.ordersLoadError")}</p>
  }

  const order = data.order
  const items = order.items || []
  const invoices: OrderInvoice[] = data.invoices || []

  return (
    <div className="space-y-4">
      <div className="overflow-x-auto">
        <table className="w-full text-sm">
          <thead>
            <tr className="text-left text-xs uppercase tracking-wider text-muted-foreground">
              <th className="py-1 pr-3">{t("orders.colDescription")}</th>
              <th className="py-1 pr-3">{t("orders.colQty")}</th>
              <th className="py-1 pr-3">{t("orders.colUnitPrice")}</th>
              <th className="py-1 pr-3">{t("orders.colLineSubtotal")}</th>
              <th className="py-1 pr-3">{t("orders.colLineDiscount")}</th>
              <th className="py-1 pr-3">{t("orders.colLineTax")}</th>
              <th className="py-1">{t("orders.colLineTotal")}</th>
            </tr>
          </thead>
          <tbody>
            {items.length === 0 ? (
              <tr>
                <td colSpan={7} className="py-2 text-muted-foreground">
                  {t("orders.itemsEmpty")}
                </td>
              </tr>
            ) : (
              items.map((it) => (
                <tr key={it.id} className="border-t">
                  <td className="py-1.5 pr-3 font-medium">{it.description || it.sku || it.product_id || "-"}</td>
                  <td className="py-1.5 pr-3">{it.quantity}</td>
                  <td className="py-1.5 pr-3">{formatMinor(it.unit_amount_minor, order.currency)}</td>
                  <td className="py-1.5 pr-3">{formatMinor(it.line_subtotal_minor, order.currency)}</td>
                  <td className="py-1.5 pr-3">{formatMinor(it.line_discount_minor, order.currency)}</td>
                  <td className="py-1.5 pr-3">{formatMinor(it.line_tax_minor, order.currency)}</td>
                  <td className="py-1.5 font-medium">{formatMinor(it.line_total_minor, order.currency)}</td>
                </tr>
              ))
            )}
          </tbody>
        </table>
      </div>

      <Separator />

      <div className="grid gap-4 sm:grid-cols-2">
        <div className="space-y-1.5 text-sm">
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
                {t("orders.coupon")}: {order.coupon_code}
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
            <span>
              {formatMinor(order.tax_minor, order.currency)}
              <span className="ml-1 text-xs text-muted-foreground">
                ({order.tax_inclusive ? t("orders.taxInclusive") : t("orders.taxExclusive")})
              </span>
            </span>
          </div>
          <Separator />
          <div className="flex justify-between font-medium">
            <span>{t("orders.colTotal")}</span>
            <span>{formatMinor(order.total_minor, order.currency)}</span>
          </div>
        </div>

        <div className="space-y-2 text-sm">
          <div className="flex items-center gap-2 font-medium">
            <Receipt className="h-4 w-4 text-muted-foreground" /> {t("orders.invoices")}
          </div>
          {invoices.length === 0 ? (
            <p className="text-xs text-muted-foreground">{t("orders.invoicesEmpty")}</p>
          ) : (
            <div className="space-y-2">
              {invoices.map((inv) => (
                <div key={inv.id} className="rounded-lg border p-2.5 space-y-1">
                  <div className="flex items-center justify-between gap-2">
                    <span className="font-medium text-xs">{inv.invoice_number}</span>
                    <Badge
                      className={statusBadgeColor(
                        inv.status === "paid" ? "paid" : inv.status === "void" ? "failed" : "pending",
                      )}
                    >
                      {t(`status.${inv.status}` as TranslationKeys)}
                    </Badge>
                  </div>
                  <div className="flex justify-between text-xs">
                    <span className="text-muted-foreground">{t("orders.colTotal")}</span>
                    <span>{formatMinor(inv.total_minor, inv.currency)}</span>
                  </div>
                  <div className="flex justify-between text-xs text-muted-foreground">
                    <span>{t("orders.issuedAt")}</span>
                    <span>{formatDate(inv.issued_at)}</span>
                  </div>
                </div>
              ))}
            </div>
          )}
        </div>
      </div>
    </div>
  )
}
