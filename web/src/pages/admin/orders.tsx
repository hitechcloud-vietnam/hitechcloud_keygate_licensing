import { useMutation, useQuery } from "@tanstack/react-query"
import { Calculator, Eye, Plus, ShoppingCart } from "lucide-react"
import { useState } from "react"
import { Link, useNavigate } from "react-router-dom"
import { ListEmptyState } from "@/components/empty-state"
import { ExportCsvButton } from "@/components/export-csv"
import { showToast, toastError } from "@/components/toast"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
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
import {
  Dialog,
  DialogBody,
  DialogContent,
  DialogDescription,
  DialogFooter,
  DialogHeader,
  DialogTitle,
} from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { type TranslationKeys, useI18n } from "@/i18n"
import { admin, type Order, type QuoteRequest, type QuoteResult } from "@/lib/api"
import { formatBps, formatMinor, parseIntStrict } from "@/lib/money"
import { formatDate } from "@/lib/utils"

const ORDER_STATUSES = ["pending", "paid", "failed", "refunded"] as const

export default function OrdersPage() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const [search, setSearch] = useState("")
  const [statusFilter, setStatusFilter] = useState<string>("")
  const [quoting, setQuoting] = useState(false)
  const pg = useServerPagination(10, [search, statusFilter])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "orders", search, statusFilter, pg.page, pg.pageSize],
    queryFn: () => admin.listOrders({ search, status: statusFilter || undefined, ...pg.params }),
  })

  const { items: orders, total, totalPages } = pg.from(data, data?.orders)

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight sr-only md:not-sr-only">{t("orders.title")}</h1>
          <p className="text-muted-foreground">{t("orders.subtitle")}</p>
        </div>
        <div className="flex items-center gap-2">
          <ExportCsvButton
            filename="orders"
            columns={[
              t("orders.orderNumber"),
              t("orders.customer"),
              t("common.status"),
              t("orders.colTotal"),
              t("orders.coupon"),
              t("common.created"),
            ]}
            rows={orders.map((o) => [
              o.order_number,
              o.customer_email,
              o.status,
              formatMinor(o.total_minor, o.currency),
              o.coupon_code || "",
              o.created_at,
            ])}
          />
          <Button variant="outline" onClick={() => setQuoting(true)}>
            <Calculator className="h-4 w-4 mr-2" /> {t("orders.quoteOpen")}
          </Button>
        </div>
      </div>

      <div className="flex flex-wrap gap-3">
        <Input
          placeholder={t("common.search")}
          aria-label={t("common.search")}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full sm:w-64"
        />
        <Select value={statusFilter} onValueChange={(v) => setStatusFilter(v === "all" ? "" : v)}>
          <SelectTrigger className="w-full sm:w-48" aria-label={t("common.status")}>
            <SelectValue placeholder={t("filter.allStatuses")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("filter.allStatuses")}</SelectItem>
            {ORDER_STATUSES.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`status.${s}` as TranslationKeys)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-32 animate-pulse bg-muted rounded-lg" />
          ) : orders.length === 0 ? (
            // The ledger fills from paid checkouts, so an empty view
            // explains that — while a filtered one offers the clear.
            // The price preview is the one thing to do while waiting.
            <ListEmptyState
              icon={ShoppingCart}
              title={t("orders.empty")}
              description={t("empty.orders.desc")}
              action={{ label: t("orders.quoteOpen"), onClick: () => setQuoting(true) }}
              filtered={search !== "" || statusFilter !== ""}
              filteredTitle={t("filter.noMatches")}
              filteredDescription={t("empty.filteredDesc")}
              clearLabel={t("common.clearFilters")}
              onClearFilters={() => {
                setSearch("")
                setStatusFilter("")
              }}
            />
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableHead>{t("orders.orderNumber")}</DataTableHead>
                    <DataTableHead>{t("orders.customer")}</DataTableHead>
                    <DataTableHead>{t("common.status")}</DataTableHead>
                    <DataTableHead>{t("orders.colTotal")}</DataTableHead>
                    <DataTableHead>{t("orders.coupon")}</DataTableHead>
                    <DataTableHead>{t("common.created")}</DataTableHead>
                    <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {orders.map((o: Order) => (
                    <DataTableRow key={o.id}>
                      <DataTableCell>
                        <Link to={`/admin/orders/${o.id}`} className="font-medium hover:underline">
                          {o.order_number}
                        </Link>
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">{o.customer_email}</DataTableCell>
                      <DataTableCell>
                        <Badge className={statusBadgeColor(o.status)}>
                          {t(`status.${o.status}` as TranslationKeys)}
                        </Badge>
                      </DataTableCell>
                      <DataTableCell className="font-medium">{formatMinor(o.total_minor, o.currency)}</DataTableCell>
                      <DataTableCell className="text-muted-foreground">{o.coupon_code || "-"}</DataTableCell>
                      <DataTableCell className="text-muted-foreground">{formatDate(o.created_at)}</DataTableCell>
                      <DataTableCell>
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("orders.viewDetail")}
                            aria-label={t("orders.viewDetail")}
                            onClick={() => navigate(`/admin/orders/${o.id}`)}
                          >
                            <Eye className="h-4 w-4" />
                          </Button>
                        </div>
                      </DataTableCell>
                    </DataTableRow>
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

      {quoting && <QuoteDialog open onClose={() => setQuoting(false)} />}
    </div>
  )
}

// The four order statuses each read differently on a badge: money on
// its way in is not money on its way back out.
export function statusBadgeColor(status: string): string {
  switch (status) {
    case "paid":
      return "bg-emerald-100 text-emerald-800"
    case "pending":
      return "bg-amber-100 text-amber-800"
    case "refunded":
      return "bg-blue-100 text-blue-800"
    case "failed":
      return "bg-red-100 text-red-700"
    default:
      return "bg-gray-100 text-gray-800"
  }
}

type QuoteLineForm = { key: number; description: string; qty: string; unitMinor: string }

let nextLineKey = 1

// QuoteDialog prices lines through POST /admin/quotes — the same
// calculation a checkout would run — without writing an order. It is
// how an admin can check a coupon or a tax rate before customers meet
// it. Everything is integer minor units on the way in and out.
function QuoteDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useI18n()
  const [currency, setCurrency] = useState("USD")
  const [couponCode, setCouponCode] = useState("")
  const [taxInclusive, setTaxInclusive] = useState(false)
  const [selected, setSelected] = useState<Record<string, boolean>>({})
  const [lines, setLines] = useState<QuoteLineForm[]>([
    { key: nextLineKey++, description: "", qty: "1", unitMinor: "" },
  ])
  const [result, setResult] = useState<QuoteResult | null>(null)

  const { data: ratesData } = useQuery({
    queryKey: ["admin", "tax-rates", "quote"],
    queryFn: () => admin.listTaxRates({ limit: 200 }),
  })
  const rates = (ratesData?.tax_rates || []).filter((r) => r.active)

  const quoteMut = useMutation({
    mutationFn: (body: QuoteRequest) => admin.quoteOrder(body),
    onSuccess: (res) => setResult(res),
    onError: (e: Error) => toastError(e),
  })

  const setLine = (key: number, patch: Partial<QuoteLineForm>) =>
    setLines((ls) => ls.map((l) => (l.key === key ? { ...l, ...patch } : l)))

  const submit = () => {
    const parsed = lines.map((l) => {
      const qty = parseIntStrict(l.qty)
      const unit = parseIntStrict(l.unitMinor)
      return { description: l.description.trim(), qty, unit }
    })
    if (parsed.some((p) => p.qty === null || p.qty < 1 || p.unit === null)) {
      return showToast(t("orders.quoteErrLine"), "error")
    }
    const taxRates = rates
      .filter((r) => selected[r.id])
      .map((r) => ({ basis_points: r.basis_points, jurisdiction: r.jurisdiction }))
    quoteMut.mutate({
      lines: parsed.map((p) => ({
        description: p.description,
        quantity: p.qty as number,
        unit_amount_minor: p.unit as number,
      })),
      currency: currency.trim().toUpperCase(),
      coupon_code: couponCode.trim() || undefined,
      tax_inclusive: taxInclusive,
      tax_rates: taxRates,
    })
  }

  return (
    <Dialog open={open} onOpenChange={onClose}>
      <DialogContent className="max-w-2xl h-[min(720px,85vh)]">
        <DialogHeader>
          <DialogTitle>{t("orders.quoteTitle")}</DialogTitle>
          <DialogDescription>{t("orders.quoteDesc")}</DialogDescription>
        </DialogHeader>
        <div className="flex min-h-0 flex-1 flex-col gap-4">
          <DialogBody className="space-y-4">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-3">
              <div className="space-y-2">
                <Label>{t("orders.quoteCurrency")}</Label>
                <Input value={currency} onChange={(e) => setCurrency(e.target.value)} maxLength={3} placeholder="USD" />
              </div>
              <div className="space-y-2">
                <Label>{t("orders.quoteCoupon")}</Label>
                <Input value={couponCode} onChange={(e) => setCouponCode(e.target.value)} />
              </div>
              <div className="flex items-end gap-3 pb-2">
                <input
                  type="checkbox"
                  id="quote-inclusive"
                  checked={taxInclusive}
                  onChange={(e) => setTaxInclusive(e.target.checked)}
                  className="h-4 w-4 rounded border-input accent-primary"
                />
                <Label htmlFor="quote-inclusive" className="font-normal">
                  {t("orders.quoteInclusive")}
                </Label>
              </div>
            </div>

            <div className="space-y-2">
              <div className="flex items-center justify-between">
                <Label>{t("orders.quoteLines")}</Label>
                <Button
                  type="button"
                  variant="outline"
                  size="sm"
                  onClick={() =>
                    setLines((ls) => [...ls, { key: nextLineKey++, description: "", qty: "1", unitMinor: "" }])
                  }
                >
                  <Plus className="h-4 w-4 mr-1" /> {t("orders.quoteAddLine")}
                </Button>
              </div>
              {lines.map((l) => (
                <div key={l.key} className="flex flex-wrap items-end gap-2">
                  <div className="grow space-y-1 min-w-40">
                    <Label className="text-xs">{t("orders.quoteDescription")}</Label>
                    <Input value={l.description} onChange={(e) => setLine(l.key, { description: e.target.value })} />
                  </div>
                  <div className="w-20 space-y-1">
                    <Label className="text-xs">{t("orders.quoteQty")}</Label>
                    <Input
                      value={l.qty}
                      onChange={(e) => setLine(l.key, { qty: e.target.value })}
                      inputMode="numeric"
                    />
                  </div>
                  <div className="w-32 space-y-1">
                    <Label className="text-xs">{t("orders.quoteUnitMinor")}</Label>
                    <Input
                      value={l.unitMinor}
                      onChange={(e) => setLine(l.key, { unitMinor: e.target.value })}
                      inputMode="numeric"
                    />
                  </div>
                  <Button
                    type="button"
                    variant="ghost"
                    size="sm"
                    disabled={lines.length === 1}
                    onClick={() => setLines((ls) => ls.filter((x) => x.key !== l.key))}
                  >
                    {t("orders.quoteRemoveLine")}
                  </Button>
                </div>
              ))}
            </div>

            <div className="space-y-2">
              <Label>{t("orders.quoteTaxRates")}</Label>
              {rates.length === 0 ? (
                <p className="text-xs text-muted-foreground">{t("orders.quoteNoRates")}</p>
              ) : (
                <div className="flex flex-wrap gap-x-4 gap-y-2">
                  {rates.map((r) => (
                    <label key={r.id} className="flex items-center gap-2 text-sm">
                      <input
                        type="checkbox"
                        checked={!!selected[r.id]}
                        onChange={(e) => setSelected((s) => ({ ...s, [r.id]: e.target.checked }))}
                        className="h-4 w-4 rounded border-input accent-primary"
                      />
                      {r.jurisdiction} ({formatBps(r.basis_points)})
                    </label>
                  ))}
                </div>
              )}
            </div>

            {result && (
              <div className="rounded-lg border p-4 space-y-2">
                <p className="text-sm font-medium">{t("orders.quoteResult")}</p>
                <div className="grid grid-cols-2 gap-x-8 gap-y-1 text-sm sm:grid-cols-4">
                  <span className="text-muted-foreground">{t("orders.subtotal")}</span>
                  <span>{formatMinor(result.subtotal_minor, result.currency)}</span>
                  <span className="text-muted-foreground">{t("orders.discount")}</span>
                  <span>{formatMinor(result.discount_minor, result.currency)}</span>
                  <span className="text-muted-foreground">{t("orders.tax")}</span>
                  <span>{formatMinor(result.tax_minor, result.currency)}</span>
                  <span className="text-muted-foreground">{t("orders.colTotal")}</span>
                  <span className="font-medium">{formatMinor(result.total_minor, result.currency)}</span>
                </div>
                {result.applied_coupon && (
                  <p className="text-xs text-muted-foreground">
                    {t("orders.quoteAppliedCoupon")}: {result.applied_coupon.code}
                  </p>
                )}
              </div>
            )}
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.close")}
            </Button>
            <Button type="button" onClick={submit} disabled={quoteMut.isPending}>
              {quoteMut.isPending ? t("common.loading") : t("orders.quoteRun")}
            </Button>
          </DialogFooter>
        </div>
      </DialogContent>
    </Dialog>
  )
}
