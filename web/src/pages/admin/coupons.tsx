import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Pencil, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { ExportCsvButton } from "@/components/export-csv"
import { showToast, toastError } from "@/components/toast"
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
import { Card, CardContent } from "@/components/ui/card"
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableEmpty,
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
import { useI18n } from "@/i18n"
import { admin, type Coupon, type CouponInput } from "@/lib/api"
import { bpsToPercentString, formatBps, formatMinor, parseIntStrict, percentStringToBps } from "@/lib/money"
import { boolColor, formatDate } from "@/lib/utils"

// ─── datetime-local ⇄ RFC 3339 ───
//
// The validity window travels as RFC 3339 timestamps (Go time.Time),
// but an admin fills in a wall-clock box. Both directions go through
// the browser's local zone: what is typed is what is meant.

function toLocalInputValue(iso: string | undefined): string {
  if (!iso) return ""
  const d = new Date(iso)
  if (Number.isNaN(d.getTime())) return ""
  const pad = (n: number) => String(n).padStart(2, "0")
  return `${d.getFullYear()}-${pad(d.getMonth() + 1)}-${pad(d.getDate())}T${pad(d.getHours())}:${pad(d.getMinutes())}`
}

function fromLocalInputValue(v: string): string | undefined {
  if (!v) return undefined
  const d = new Date(v)
  return Number.isNaN(d.getTime()) ? undefined : d.toISOString()
}

export default function CouponsPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [search, setSearch] = useState("")
  const pg = useServerPagination(10, [search])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "coupons", search, pg.page, pg.pageSize],
    queryFn: () => admin.listCoupons({ search, ...pg.params }),
  })
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<Coupon | null>(null)
  const [deleting, setDeleting] = useState<Coupon | null>(null)

  const { items: coupons, total, totalPages } = pg.from(data, data?.coupons)

  const deleteMut = useMutation({
    mutationFn: (id: string) => admin.deleteCoupon(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "coupons"] })
      showToast(t("toast.couponDeleted"), "success")
      setDeleting(null)
    },
    onError: (e: Error) => toastError(e),
  })

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight sr-only md:not-sr-only">{t("coupons.title")}</h1>
          <p className="text-muted-foreground">{t("coupons.subtitle")}</p>
        </div>
        <div className="flex items-center gap-2">
          <ExportCsvButton
            filename="coupons"
            columns={[
              t("coupons.code"),
              t("common.type"),
              t("coupons.colValue"),
              t("coupons.colValidity"),
              t("coupons.colRedemptions"),
              t("common.status"),
            ]}
            rows={coupons.map((c) => [
              c.code,
              c.type === "percent_off" ? t("coupons.percentOff") : t("coupons.fixedOff"),
              c.type === "percent_off" ? formatBps(c.value_bps) : formatMinor(c.value_minor, c.currency || ""),
              c.starts_at || c.ends_at ? `${formatDate(c.starts_at)} – ${formatDate(c.ends_at)}` : t("coupons.anyTime"),
              `${c.times_redeemed} / ${c.max_redemptions > 0 ? c.max_redemptions : t("coupons.unlimited")}`,
              c.active ? t("common.active") : t("common.inactive"),
            ])}
          />
          <Button onClick={() => setCreating(true)}>
            <Plus className="h-4 w-4 mr-2" /> {t("coupons.new")}
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
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-32 animate-pulse bg-muted rounded-lg" />
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableHead>{t("coupons.code")}</DataTableHead>
                    <DataTableHead>{t("common.type")}</DataTableHead>
                    <DataTableHead>{t("coupons.colValue")}</DataTableHead>
                    <DataTableHead>{t("coupons.colValidity")}</DataTableHead>
                    <DataTableHead>{t("coupons.colRedemptions")}</DataTableHead>
                    <DataTableHead>{t("common.status")}</DataTableHead>
                    <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {coupons.length === 0 && <DataTableEmpty colSpan={7} message={t("coupons.empty")} />}
                  {coupons.map((c: Coupon) => (
                    <DataTableRow key={c.id}>
                      <DataTableCell className="font-medium">{c.code}</DataTableCell>
                      <DataTableCell>
                        <Badge variant="secondary">
                          {c.type === "percent_off" ? t("coupons.percentOff") : t("coupons.fixedOff")}
                        </Badge>
                      </DataTableCell>
                      <DataTableCell>
                        {c.type === "percent_off"
                          ? formatBps(c.value_bps)
                          : formatMinor(c.value_minor, c.currency || "")}
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">
                        {c.starts_at || c.ends_at ? (
                          <span>
                            {formatDate(c.starts_at)} – {formatDate(c.ends_at)}
                          </span>
                        ) : (
                          t("coupons.anyTime")
                        )}
                      </DataTableCell>
                      <DataTableCell>
                        {c.times_redeemed} / {c.max_redemptions > 0 ? c.max_redemptions : t("coupons.unlimited")}
                      </DataTableCell>
                      <DataTableCell>
                        <Badge className={boolColor(c.active)}>
                          {c.active ? t("common.active") : t("common.inactive")}
                        </Badge>
                      </DataTableCell>
                      <DataTableCell>
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("common.edit")}
                            aria-label={t("common.edit")}
                            onClick={() => setEditing(c)}
                          >
                            <Pencil className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("common.delete")}
                            aria-label={t("common.delete")}
                            onClick={() => setDeleting(c)}
                          >
                            <Trash2 className="h-4 w-4 text-destructive" />
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

      {creating && <CouponDialog open onClose={() => setCreating(false)} />}
      {editing && <CouponDialog open onClose={() => setEditing(null)} coupon={editing} />}

      <AlertDialog open={!!deleting} onOpenChange={() => setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("common.delete")} "{deleting?.code}"?
            </AlertDialogTitle>
            <AlertDialogDescription>{t("coupons.deleteConfirm")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => deleting && deleteMut.mutate(deleting.id)}
            >
              {t("common.delete")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function CouponDialog({ open, onClose, coupon }: { open: boolean; onClose: () => void; coupon?: Coupon }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [form, setForm] = useState({
    code: coupon?.code || "",
    type: coupon?.type || "percent_off",
    // Percent discounts are typed as a percent but stored as basis
    // points; fixed discounts are typed directly in integer minor
    // units. Both round-trip through lib/money without floats.
    percent: coupon ? bpsToPercentString(coupon.value_bps) : "",
    valueMinor: coupon ? String(coupon.value_minor) : "",
    currency: coupon?.currency || "",
    startsAt: toLocalInputValue(coupon?.starts_at),
    endsAt: toLocalInputValue(coupon?.ends_at),
    maxRedemptions: String(coupon?.max_redemptions ?? 0),
    maxPerCustomer: String(coupon?.max_redemptions_per_customer ?? 0),
    minimumOrder: String(coupon?.minimum_order_minor ?? 0),
    appliesTo: coupon?.applies_to || "",
    stackable: coupon?.stackable ?? true,
    active: coupon?.active ?? true,
  })

  const set = (k: string, v: string | boolean) => setForm((f) => ({ ...f, [k]: v }))

  const createMut = useMutation({
    mutationFn: (body: CouponInput) => (coupon ? admin.updateCoupon(coupon.id, body) : admin.createCoupon(body)),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "coupons"] })
      showToast(coupon ? t("toast.couponSaved") : t("toast.couponCreated"), "success")
      onClose()
    },
    onError: (e: Error) => toastError(e),
  })

  const submit = () => {
    const code = form.code.trim()
    if (!code) return showToast(t("coupons.errCode"), "error")

    // The two types carry their value in different columns, and the
    // server refuses a type whose column does not hold it — validate
    // the pair here so the error names the field the admin filled in.
    let valueBPS = 0
    let valueMinor = 0
    if (form.type === "percent_off") {
      const bps = percentStringToBps(form.percent)
      if (bps === null || bps < 1 || bps > 10000) return showToast(t("coupons.errPercent"), "error")
      valueBPS = bps
    } else {
      const minor = parseIntStrict(form.valueMinor)
      if (minor === null || minor < 1) return showToast(t("coupons.errFixedValue"), "error")
      if (!form.currency.trim()) return showToast(t("coupons.errCurrency"), "error")
      valueMinor = minor
    }

    const maxRedemptions = parseIntStrict(form.maxRedemptions)
    const maxPerCustomer = parseIntStrict(form.maxPerCustomer)
    const minimumOrder = parseIntStrict(form.minimumOrder)
    if (maxRedemptions === null || maxPerCustomer === null || minimumOrder === null) {
      return showToast(t("coupons.errInt"), "error")
    }

    const startsAt = fromLocalInputValue(form.startsAt)
    const endsAt = fromLocalInputValue(form.endsAt)
    if (startsAt && endsAt && new Date(endsAt) <= new Date(startsAt)) {
      return showToast(t("coupons.errWindow"), "error")
    }

    const body: CouponInput = {
      code,
      type: form.type as CouponInput["type"],
      value_bps: valueBPS,
      value_minor: valueMinor,
      currency: form.currency.trim().toUpperCase(),
      max_redemptions: maxRedemptions,
      max_redemptions_per_customer: maxPerCustomer,
      minimum_order_minor: minimumOrder,
      applies_to: form.appliesTo.trim(),
      stackable: form.stackable,
      active: form.active,
    }
    // An omitted bound is "no bound" on create and "keep the stored
    // one" on update, which is the only reading both share.
    if (startsAt) body.starts_at = startsAt
    if (endsAt) body.ends_at = endsAt
    createMut.mutate(body)
  }

  return (
    <Dialog open={open} onOpenChange={onClose}>
      <DialogContent className="max-w-lg h-[min(680px,85vh)]">
        <DialogHeader>
          <DialogTitle>{coupon ? t("coupons.edit") : t("coupons.new")}</DialogTitle>
          <DialogDescription>{t("coupons.formDesc")}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
          className="flex min-h-0 flex-1 flex-col gap-4"
        >
          <DialogBody className="space-y-4">
            <div className="grid grid-cols-1 gap-4 sm:grid-cols-2">
              <div className="space-y-2">
                <Label>{t("coupons.code")}</Label>
                <Input value={form.code} onChange={(e) => set("code", e.target.value)} required />
              </div>
              <div className="space-y-2">
                <Label>{t("common.type")}</Label>
                <Select value={form.type} onValueChange={(v) => set("type", v)}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    <SelectItem value="percent_off">{t("coupons.percentOff")}</SelectItem>
                    <SelectItem value="fixed_off">{t("coupons.fixedOff")}</SelectItem>
                  </SelectContent>
                </Select>
              </div>
              {form.type === "percent_off" ? (
                <div className="space-y-2">
                  <Label>{t("coupons.percentValue")}</Label>
                  <Input
                    value={form.percent}
                    onChange={(e) => set("percent", e.target.value)}
                    inputMode="decimal"
                    required
                  />
                  <p className="text-xs text-muted-foreground">{t("coupons.percentHint")}</p>
                </div>
              ) : (
                <>
                  <div className="space-y-2">
                    <Label>{t("coupons.fixedValue")}</Label>
                    <Input
                      value={form.valueMinor}
                      onChange={(e) => set("valueMinor", e.target.value)}
                      inputMode="numeric"
                      required
                    />
                    <p className="text-xs text-muted-foreground">{t("coupons.fixedHint")}</p>
                  </div>
                  <div className="space-y-2">
                    <Label>{t("coupons.currency")}</Label>
                    <Input
                      value={form.currency}
                      onChange={(e) => set("currency", e.target.value)}
                      placeholder="USD"
                      maxLength={3}
                    />
                    <p className="text-xs text-muted-foreground">{t("coupons.currencyHint")}</p>
                  </div>
                </>
              )}
              <div className="space-y-2">
                <Label>{t("coupons.startsAt")}</Label>
                <Input type="datetime-local" value={form.startsAt} onChange={(e) => set("startsAt", e.target.value)} />
              </div>
              <div className="space-y-2">
                <Label>{t("coupons.endsAt")}</Label>
                <Input type="datetime-local" value={form.endsAt} onChange={(e) => set("endsAt", e.target.value)} />
              </div>
              <div className="space-y-2 sm:col-span-2">
                <p className="text-xs text-muted-foreground">{t("coupons.windowHint")}</p>
              </div>
              <div className="space-y-2">
                <Label>{t("coupons.maxRedemptions")}</Label>
                <Input
                  value={form.maxRedemptions}
                  onChange={(e) => set("maxRedemptions", e.target.value)}
                  inputMode="numeric"
                  min={0}
                />
              </div>
              <div className="space-y-2">
                <Label>{t("coupons.maxPerCustomer")}</Label>
                <Input
                  value={form.maxPerCustomer}
                  onChange={(e) => set("maxPerCustomer", e.target.value)}
                  inputMode="numeric"
                  min={0}
                />
              </div>
              <div className="space-y-2">
                <Label>{t("coupons.minimumOrder")}</Label>
                <Input
                  value={form.minimumOrder}
                  onChange={(e) => set("minimumOrder", e.target.value)}
                  inputMode="numeric"
                  min={0}
                />
              </div>
              <div className="space-y-2">
                <Label>{t("coupons.appliesTo")}</Label>
                <Input value={form.appliesTo} onChange={(e) => set("appliesTo", e.target.value)} />
                <p className="text-xs text-muted-foreground">{t("coupons.appliesToHint")}</p>
              </div>
              <div className="space-y-2 sm:col-span-2">
                <p className="text-xs text-muted-foreground">{t("coupons.limitsHint")}</p>
              </div>
              <div className="flex items-center gap-3">
                <input
                  type="checkbox"
                  id="coupon-stackable"
                  checked={form.stackable}
                  onChange={(e) => set("stackable", e.target.checked)}
                  className="h-4 w-4 rounded border-input accent-primary"
                />
                <Label htmlFor="coupon-stackable" className="font-normal">
                  {t("coupons.stackable")}
                </Label>
                <span className="text-xs text-muted-foreground">{t("coupons.stackableHint")}</span>
              </div>
              <div className="flex items-center gap-3">
                <input
                  type="checkbox"
                  id="coupon-active"
                  checked={form.active}
                  onChange={(e) => set("active", e.target.checked)}
                  className="h-4 w-4 rounded border-input accent-primary"
                />
                <Label htmlFor="coupon-active" className="font-normal">
                  {t("common.active")}
                </Label>
              </div>
              {/* The redemption counter moves only when a coupon is
                  redeemed, never by hand — shown so an admin can see
                  how much of a cap is used, edited nowhere. */}
              {coupon && (
                <div className="sm:col-span-2 text-xs text-muted-foreground">
                  {t("coupons.timesRedeemed")}: {coupon.times_redeemed}
                </div>
              )}
            </div>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={createMut.isPending}>
              {createMut.isPending ? t("common.loading") : t("common.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
