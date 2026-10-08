import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Eye, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { Link, useNavigate } from "react-router-dom"
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
import { type TranslationKeys, useI18n } from "@/i18n"
import { type Affiliate, ApiError, admin } from "@/lib/api"
import { formatBps, formatMinorUnits, parseIntStrict, percentStringToBps } from "@/lib/money"
import { formatDate, statusColor } from "@/lib/utils"

const AFFILIATE_STATUSES = ["active", "suspended"] as const
const COMMISSION_MODELS = ["percent", "fixed"] as const

export default function AffiliatesPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [search, setSearch] = useState("")
  const [statusFilter, setStatusFilter] = useState("")
  const pg = useServerPagination(10, [search, statusFilter])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "affiliates", search, statusFilter, pg.page, pg.pageSize],
    queryFn: () => admin.listAffiliates({ search, status: statusFilter || undefined, ...pg.params }),
  })
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Affiliate | null>(null)

  const { items: affiliates, total, totalPages } = pg.from(data, data?.affiliates)

  // Money records are never silently emptied: the refusals name what
  // is still attached so the admin suspends instead of guessing.
  const deleteMut = useMutation({
    mutationFn: (id: string) => admin.deleteAffiliate(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "affiliates"] })
      showToast(t("toast.affiliateDeleted"), "success")
      setDeleting(null)
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.code === "AFFILIATE_HAS_CONVERSIONS") {
        showToast(t("affiliates.deleteBlockedConversions"), "error")
      } else if (e instanceof ApiError && e.code === "AFFILIATE_HAS_PAYOUTS") {
        showToast(t("affiliates.deleteBlockedPayouts"), "error")
      } else {
        showToast(e.message, "error")
      }
    },
  })

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight sr-only md:not-sr-only">{t("affiliates.title")}</h1>
          <p className="text-muted-foreground">{t("affiliates.subtitle")}</p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="h-4 w-4 mr-2" /> {t("affiliates.new")}
        </Button>
      </div>

      <div className="flex flex-wrap gap-3">
        <Input
          placeholder={t("common.search")}
          value={search}
          onChange={(e) => setSearch(e.target.value)}
          className="w-full sm:w-64"
        />
        <Select value={statusFilter} onValueChange={(v) => setStatusFilter(v === "all" ? "" : v)}>
          <SelectTrigger className="w-full sm:w-48">
            <SelectValue placeholder={t("filter.allStatuses")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("filter.allStatuses")}</SelectItem>
            {AFFILIATE_STATUSES.map((s) => (
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
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableHead>{t("common.name")}</DataTableHead>
                    <DataTableHead>{t("resellers.contactEmail")}</DataTableHead>
                    <DataTableHead>{t("common.status")}</DataTableHead>
                    <DataTableHead>{t("affiliates.commissionModel")}</DataTableHead>
                    <DataTableHead>{t("common.created")}</DataTableHead>
                    <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {affiliates.length === 0 && <DataTableEmpty colSpan={6} message={t("affiliates.empty")} />}
                  {affiliates.map((a: Affiliate) => (
                    <DataTableRow key={a.id}>
                      <DataTableCell>
                        <Link to={`/admin/affiliates/${a.id}`} className="font-medium hover:underline">
                          {a.name}
                        </Link>
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">{a.contact_email}</DataTableCell>
                      <DataTableCell>
                        <Badge className={statusColor(a.status)}>{t(`status.${a.status}` as TranslationKeys)}</Badge>
                      </DataTableCell>
                      <DataTableCell>
                        {a.commission_model === "fixed"
                          ? `${formatMinorUnits(a.commission_minor)} (${t("common.minorUnits")})`
                          : formatBps(a.commission_bps)}
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">{formatDate(a.created_at)}</DataTableCell>
                      <DataTableCell>
                        <div className="flex justify-end gap-1">
                          <Button variant="ghost" size="icon" onClick={() => navigate(`/admin/affiliates/${a.id}`)}>
                            <Eye className="h-4 w-4" />
                          </Button>
                          <Button variant="ghost" size="icon" onClick={() => setDeleting(a)}>
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

      {creating && <AffiliateCreateDialog open onClose={() => setCreating(false)} />}

      <AlertDialog open={!!deleting} onOpenChange={() => setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("common.delete")} "{deleting?.name}"?
            </AlertDialogTitle>
            <AlertDialogDescription>{t("affiliates.deleteConfirm")}</AlertDialogDescription>
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

function AffiliateCreateDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [form, setForm] = useState({
    name: "",
    contactEmail: "",
    status: "active",
    commissionModel: "percent",
    rate: "0",
    fixedMinor: "0",
    payoutMethod: "",
    notes: "",
  })
  const set = (k: string, v: string) => setForm((f) => ({ ...f, [k]: v }))

  const createMut = useMutation({
    mutationFn: (body: {
      name: string
      contact_email: string
      status: string
      commission_model: string
      commission_bps: number
      commission_minor: number
      payout_method: string
      notes: string
    }) => admin.createAffiliate(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "affiliates"] })
      showToast(t("toast.affiliateCreated"), "success")
      onClose()
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  const submit = () => {
    const name = form.name.trim()
    const contactEmail = form.contactEmail.trim()
    if (!name) return showToast(t("affiliates.errName"), "error")
    if (!contactEmail.includes("@")) return showToast(t("affiliates.errEmail"), "error")
    // Both commission numbers are sent whatever the model — the
    // backend stores both so switching models later never loses a
    // number. The rate is bps, the fixed amount minor units.
    const bps = percentStringToBps(form.rate)
    if (bps === null || bps > 10000) return showToast(t("affiliates.errRate"), "error")
    const fixed = parseIntStrict(form.fixedMinor)
    if (fixed === null) return showToast(t("affiliates.errFixed"), "error")
    createMut.mutate({
      name,
      contact_email: contactEmail,
      status: form.status,
      commission_model: form.commissionModel,
      commission_bps: bps,
      commission_minor: fixed,
      payout_method: form.payoutMethod.trim(),
      notes: form.notes.trim(),
    })
  }

  return (
    <Dialog open={open} onOpenChange={onClose}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("affiliates.createTitle")}</DialogTitle>
          <DialogDescription>{t("affiliates.formDesc")}</DialogDescription>
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
                <Label>{t("common.name")}</Label>
                <Input value={form.name} onChange={(e) => set("name", e.target.value)} required />
              </div>
              <div className="space-y-2">
                <Label>{t("resellers.contactEmail")}</Label>
                <Input
                  type="email"
                  value={form.contactEmail}
                  onChange={(e) => set("contactEmail", e.target.value)}
                  required
                />
              </div>
              <div className="space-y-2">
                <Label>{t("common.status")}</Label>
                <Select value={form.status} onValueChange={(v) => set("status", v)}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {AFFILIATE_STATUSES.map((s) => (
                      <SelectItem key={s} value={s}>
                        {t(`status.${s}` as TranslationKeys)}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              <div className="space-y-2">
                <Label>{t("affiliates.commissionModel")}</Label>
                <Select value={form.commissionModel} onValueChange={(v) => set("commissionModel", v)}>
                  <SelectTrigger>
                    <SelectValue />
                  </SelectTrigger>
                  <SelectContent>
                    {COMMISSION_MODELS.map((m) => (
                      <SelectItem key={m} value={m}>
                        {m === "percent" ? t("affiliates.modelPercent") : t("affiliates.modelFixed")}
                      </SelectItem>
                    ))}
                  </SelectContent>
                </Select>
              </div>
              {form.commissionModel === "percent" ? (
                <div className="space-y-2">
                  <Label>{t("affiliates.percentRate")}</Label>
                  <Input value={form.rate} onChange={(e) => set("rate", e.target.value)} inputMode="decimal" required />
                  <p className="text-xs text-muted-foreground">{t("affiliates.percentRateHint")}</p>
                </div>
              ) : (
                <div className="space-y-2">
                  <Label>{t("affiliates.fixedAmount")}</Label>
                  <Input
                    value={form.fixedMinor}
                    onChange={(e) => set("fixedMinor", e.target.value)}
                    inputMode="numeric"
                    required
                  />
                  <p className="text-xs text-muted-foreground">{t("affiliates.fixedAmountHint")}</p>
                </div>
              )}
              <div className="space-y-2 sm:col-span-2">
                <Label>{t("affiliates.payoutMethod")}</Label>
                <Input value={form.payoutMethod} onChange={(e) => set("payoutMethod", e.target.value)} />
                <p className="text-xs text-muted-foreground">{t("affiliates.payoutMethodHint")}</p>
              </div>
              <div className="space-y-2 sm:col-span-2">
                <Label>{t("common.notes")}</Label>
                <textarea
                  value={form.notes}
                  onChange={(e) => set("notes", e.target.value)}
                  rows={2}
                  className="flex w-full rounded-md border border-input bg-background px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
                />
              </div>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={createMut.isPending}>
              {createMut.isPending ? t("common.loading") : t("common.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
