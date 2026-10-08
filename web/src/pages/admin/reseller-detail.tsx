import { keepPreviousData, useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, Banknote, Check, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { Link, useParams } from "react-router-dom"
import { CopyableId } from "@/components/copyable-id"
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
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
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
import { PICKER_PAGE, SearchableSelect } from "@/components/ui/searchable-select"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { type TranslationKeys, useI18n } from "@/i18n"
import { admin, type Commission, type License, type Reseller, type ResellerPriceOverride } from "@/lib/api"
import { errorMessage } from "@/lib/errors"
import {
  bpsToPercentString,
  formatBps,
  formatMinor,
  formatMinorUnits,
  parseIntStrict,
  percentStringToBps,
} from "@/lib/money"
import { formatDate, statusColor } from "@/lib/utils"
import { ledgerBadgeColor } from "@/pages/admin/resellers"

const RESELLER_STATUSES = ["active", "suspended"] as const
const COMMISSION_STATUSES = ["accrued", "approved", "paid", "cancelled"] as const

export default function ResellerDetailPage() {
  const { t } = useI18n()
  const { id = "" } = useParams()
  const { data, isLoading, error } = useQuery({
    queryKey: ["admin", "resellers", id],
    queryFn: () => admin.getReseller(id),
    enabled: !!id,
  })

  if (isLoading) {
    return (
      <div className="space-y-6">
        <div className="h-8 w-64 animate-pulse bg-muted rounded-lg" />
        <div className="h-64 animate-pulse bg-muted rounded-lg" />
      </div>
    )
  }

  const reseller = data?.reseller
  if (!reseller) {
    return (
      <div className="space-y-6">
        <Link
          to="/admin/resellers"
          className="inline-flex items-center gap-2 text-sm text-muted-foreground hover:underline"
        >
          <ArrowLeft className="h-4 w-4" /> {t("resellers.backToList")}
        </Link>
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            {error ? (error instanceof Error ? errorMessage(error) : String(error)) : t("resellers.notFound")}
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <Link
        to="/admin/resellers"
        className="inline-flex items-center gap-2 text-sm text-muted-foreground hover:underline"
      >
        <ArrowLeft className="h-4 w-4" /> {t("resellers.backToList")}
      </Link>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-3">
            <h1 className="text-2xl font-bold tracking-tight">{reseller.name}</h1>
            <Badge className={statusColor(reseller.status)}>{t(`status.${reseller.status}` as TranslationKeys)}</Badge>
          </div>
          <p className="text-muted-foreground">
            {reseller.contact_email} · {t("resellers.licenseCount", { count: data?.license_count ?? 0 })}
          </p>
        </div>
      </div>

      {/* key on updated_at so the form re-reads the stored values after
          a save refetches the account. */}
      <ProfileForm key={reseller.updated_at} reseller={reseller} />
      <LicensesCard resellerId={reseller.id} />
      <CommissionsCard resellerId={reseller.id} contractBps={reseller.commission_bps} />
      <PricesCard resellerId={reseller.id} />
    </div>
  )
}

// ─── Profile ───

function ProfileForm({ reseller }: { reseller: Reseller }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [form, setForm] = useState({
    name: reseller.name,
    contactEmail: reseller.contact_email,
    status: reseller.status,
    rate: bpsToPercentString(reseller.commission_bps),
    notes: reseller.notes,
  })
  const set = (k: string, v: string) => setForm((f) => ({ ...f, [k]: v }))

  const saveMut = useMutation({
    mutationFn: (body: {
      name: string
      contact_email: string
      status: string
      commission_bps: number
      notes: string
    }) => admin.updateReseller(reseller.id, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "resellers"] })
      showToast(t("toast.resellerSaved"), "success")
    },
    onError: (e: Error) => toastError(e),
  })

  const submit = () => {
    const name = form.name.trim()
    const contactEmail = form.contactEmail.trim()
    if (!name) return showToast(t("resellers.errName"), "error")
    if (!contactEmail.includes("@")) return showToast(t("resellers.errEmail"), "error")
    const bps = percentStringToBps(form.rate)
    if (bps === null || bps > 10000) return showToast(t("resellers.errRate"), "error")
    saveMut.mutate({
      name,
      contact_email: contactEmail,
      status: form.status,
      commission_bps: bps,
      notes: form.notes.trim(),
    })
  }

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t("resellerDetail.profile")}</CardTitle>
      </CardHeader>
      <CardContent>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
          className="space-y-4"
        >
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
                  {RESELLER_STATUSES.map((s) => (
                    <SelectItem key={s} value={s}>
                      {t(`status.${s}` as TranslationKeys)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="space-y-2">
              <Label>{t("resellers.commissionRate")}</Label>
              <Input value={form.rate} onChange={(e) => set("rate", e.target.value)} inputMode="decimal" required />
              <p className="text-xs text-muted-foreground">{t("resellers.rateHint")}</p>
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
          <div className="flex justify-end">
            <Button type="submit" disabled={saveMut.isPending}>
              {saveMut.isPending ? t("common.loading") : t("common.save")}
            </Button>
          </div>
        </form>
      </CardContent>
    </Card>
  )
}

// ─── Allocated licences ───

function LicensesCard({ resellerId }: { resellerId: string }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const pg = useServerPagination(5, [])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "resellers", resellerId, "licenses", pg.page, pg.pageSize],
    queryFn: () => admin.listResellerLicenses(resellerId, pg.params),
  })
  const [allocating, setAllocating] = useState(false)
  const [deallocating, setDeallocating] = useState<License | null>(null)

  const { items: licenses, total, totalPages } = pg.from(data, data?.licenses)

  const deallocateMut = useMutation({
    mutationFn: (licenseId: string) => admin.deallocateResellerLicense(resellerId, licenseId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "resellers"] })
      showToast(t("toast.licenseDeallocated"), "success")
      setDeallocating(null)
    },
    onError: (e: Error) => toastError(e),
  })

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0">
        <CardTitle className="text-base">{t("resellerDetail.licenses")}</CardTitle>
        <Button size="sm" variant="outline" onClick={() => setAllocating(true)}>
          <Plus className="h-4 w-4 mr-2" /> {t("resellerDetail.allocate")}
        </Button>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="h-24 animate-pulse bg-muted rounded-lg" />
        ) : (
          <>
            <DataTable>
              <DataTableHeader>
                <DataTableRow>
                  <DataTableHead>{t("resellerDetail.colLicense")}</DataTableHead>
                  <DataTableHead>{t("common.email")}</DataTableHead>
                  <DataTableHead>{t("common.status")}</DataTableHead>
                  <DataTableHead>{t("licenses.validUntil")}</DataTableHead>
                  <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
                </DataTableRow>
              </DataTableHeader>
              <DataTableBody>
                {licenses.length === 0 && <DataTableEmpty colSpan={5} message={t("resellerDetail.licensesEmpty")} />}
                {licenses.map((l: License) => (
                  <DataTableRow key={l.id}>
                    <DataTableCell>
                      <CopyableId id={l.id} />
                    </DataTableCell>
                    <DataTableCell className="text-muted-foreground">{l.email}</DataTableCell>
                    <DataTableCell>
                      <Badge className={statusColor(l.status)}>{l.status}</Badge>
                    </DataTableCell>
                    <DataTableCell className="text-muted-foreground">{formatDate(l.valid_until)}</DataTableCell>
                    <DataTableCell>
                      <div className="flex justify-end gap-1">
                        <Button variant="ghost" size="icon" onClick={() => setDeallocating(l)}>
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

      {allocating && <AllocateDialog resellerId={resellerId} onClose={() => setAllocating(false)} />}

      <AlertDialog open={!!deallocating} onOpenChange={() => setDeallocating(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("resellerDetail.deallocateTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("resellerDetail.deallocateDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={() => deallocating && deallocateMut.mutate(deallocating.id)}>
              {t("resellerDetail.deallocate")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}

function AllocateDialog({ resellerId, onClose }: { resellerId: string; onClose: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [licenseId, setLicenseId] = useState("")
  const [search, setSearch] = useState("")
  const { data, isFetching } = useQuery({
    queryKey: ["admin", "licenses", "picker", search],
    queryFn: () => admin.listLicenses({ search, limit: PICKER_PAGE }),
    placeholderData: keepPreviousData,
  })
  const licenses = data?.licenses || []

  const allocateMut = useMutation({
    mutationFn: () => admin.allocateResellerLicense(resellerId, licenseId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "resellers"] })
      showToast(t("toast.licenseAllocated"), "success")
      onClose()
    },
    onError: (e: Error) => toastError(e),
  })

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("resellerDetail.allocateTitle")}</DialogTitle>
          <DialogDescription>{t("resellerDetail.allocateDesc")}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (!licenseId) return showToast(t("resellerDetail.errLicenseRequired"), "error")
            allocateMut.mutate()
          }}
          className="flex min-h-0 flex-1 flex-col gap-4"
        >
          <DialogBody className="space-y-4">
            <div className="space-y-2">
              <Label>{t("resellerDetail.licensePicker")}</Label>
              <SearchableSelect
                value={licenseId}
                onChange={setLicenseId}
                options={licenses.map((l: License) => ({
                  id: l.id,
                  label: l.email || l.id,
                  hint: l.id.slice(0, 8),
                }))}
                total={data?.total || 0}
                search={search}
                onSearchChange={setSearch}
                current={null}
                searchPlaceholder={t("filter.searchLicenses")}
                emptyLabel={t("filter.noMatches")}
                moreLabel={(hidden) => t("filter.moreMatches", { count: hidden })}
                className="w-full"
                loading={isFetching}
              />
            </div>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={allocateMut.isPending}>
              {allocateMut.isPending ? t("common.loading") : t("resellerDetail.allocate")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ─── Commission ledger ───

function CommissionsCard({ resellerId, contractBps }: { resellerId: string; contractBps: number }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [statusFilter, setStatusFilter] = useState("")
  const pg = useServerPagination(5, [statusFilter])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "resellers", resellerId, "commissions", statusFilter, pg.page, pg.pageSize],
    queryFn: () => admin.listCommissions(resellerId, { status: statusFilter || undefined, ...pg.params }),
  })
  const [accruing, setAccruing] = useState(false)
  const [paying, setPaying] = useState<Commission | null>(null)

  const { items: rows, total, totalPages } = pg.from(data, data?.commissions)

  const paidMut = useMutation({
    mutationFn: (commissionId: string) => admin.markCommissionPaid(resellerId, commissionId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "resellers", resellerId, "commissions"] })
      showToast(t("toast.commissionPaid"), "success")
      setPaying(null)
    },
    onError: (e: Error) => toastError(e),
  })

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 gap-3">
        <CardTitle className="text-base">{t("resellerDetail.commissions")}</CardTitle>
        <div className="flex items-center gap-3">
          <Select value={statusFilter} onValueChange={(v) => setStatusFilter(v === "all" ? "" : v)}>
            <SelectTrigger className="w-40">
              <SelectValue placeholder={t("filter.allStatuses")} />
            </SelectTrigger>
            <SelectContent>
              <SelectItem value="all">{t("filter.allStatuses")}</SelectItem>
              {COMMISSION_STATUSES.map((s) => (
                <SelectItem key={s} value={s}>
                  {t(`status.${s}` as TranslationKeys)}
                </SelectItem>
              ))}
            </SelectContent>
          </Select>
          <Button size="sm" variant="outline" onClick={() => setAccruing(true)}>
            <Plus className="h-4 w-4 mr-2" /> {t("resellerDetail.accrue")}
          </Button>
        </div>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="h-24 animate-pulse bg-muted rounded-lg" />
        ) : (
          <>
            <DataTable>
              <DataTableHeader>
                <DataTableRow>
                  <DataTableHead>{t("resellerDetail.colOrder")}</DataTableHead>
                  <DataTableHead>{t("resellerDetail.colBasis")}</DataTableHead>
                  <DataTableHead>{t("resellerDetail.colRate")}</DataTableHead>
                  <DataTableHead>{t("resellerDetail.colAmount")}</DataTableHead>
                  <DataTableHead>{t("common.status")}</DataTableHead>
                  <DataTableHead>{t("orders.paidAt")}</DataTableHead>
                  <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
                </DataTableRow>
              </DataTableHeader>
              <DataTableBody>
                {rows.length === 0 && <DataTableEmpty colSpan={7} message={t("resellerDetail.commissionsEmpty")} />}
                {rows.map((cm: Commission) => (
                  <DataTableRow key={cm.id}>
                    <DataTableCell className="font-medium">
                      <CopyableId id={cm.order_id} />
                    </DataTableCell>
                    <DataTableCell>{formatMinorUnits(cm.basis_minor)}</DataTableCell>
                    <DataTableCell>{formatBps(cm.bps)}</DataTableCell>
                    <DataTableCell className="font-medium">{formatMinorUnits(cm.amount_minor)}</DataTableCell>
                    <DataTableCell>
                      <Badge className={ledgerBadgeColor(cm.status)}>
                        {t(`status.${cm.status}` as TranslationKeys)}
                      </Badge>
                    </DataTableCell>
                    <DataTableCell className="text-muted-foreground">{formatDate(cm.paid_at)}</DataTableCell>
                    <DataTableCell>
                      <div className="flex justify-end gap-1">
                        {(cm.status === "accrued" || cm.status === "approved") && (
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("resellerDetail.markPaid")}
                            onClick={() => setPaying(cm)}
                          >
                            <Check className="h-4 w-4 text-emerald-600" />
                          </Button>
                        )}
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

      {accruing && (
        <AccrueDialog resellerId={resellerId} contractBps={contractBps} onClose={() => setAccruing(false)} />
      )}

      <AlertDialog open={!!paying} onOpenChange={() => setPaying(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("resellerDetail.markPaidTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("resellerDetail.markPaidDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={() => paying && paidMut.mutate(paying.id)}>
              {t("resellerDetail.markPaid")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}

function AccrueDialog({
  resellerId,
  contractBps,
  onClose,
}: {
  resellerId: string
  contractBps: number
  onClose: () => void
}) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [form, setForm] = useState({ orderId: "", basis: "", rate: bpsToPercentString(contractBps) })
  const set = (k: string, v: string) => setForm((f) => ({ ...f, [k]: v }))

  const accrueMut = useMutation({
    mutationFn: (body: { order_id: string; basis_minor: number; bps?: number }) =>
      admin.accrueCommission(resellerId, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "resellers", resellerId, "commissions"] })
      showToast(t("toast.commissionAccrued"), "success")
      onClose()
    },
    onError: (e: Error) => toastError(e),
  })

  const submit = () => {
    const orderId = form.orderId.trim()
    if (!orderId) return showToast(t("resellerDetail.errOrderId"), "error")
    const basis = parseIntStrict(form.basis)
    if (basis === null) return showToast(t("resellerDetail.errBasis"), "error")
    // Blank rate = the reseller's contract rate (the server's default);
    // a stated rate travels as integer basis points.
    let bps: number | undefined
    if (form.rate.trim() !== "") {
      const parsed = percentStringToBps(form.rate)
      if (parsed === null || parsed > 10000) return showToast(t("resellerDetail.errRate"), "error")
      bps = parsed
    }
    accrueMut.mutate({ order_id: orderId, basis_minor: basis, ...(bps !== undefined ? { bps } : {}) })
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("resellerDetail.accrueTitle")}</DialogTitle>
          <DialogDescription>{t("resellerDetail.accrueDesc")}</DialogDescription>
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
              <div className="space-y-2 sm:col-span-2">
                <Label>{t("resellerDetail.accrueOrderId")}</Label>
                <Input value={form.orderId} onChange={(e) => set("orderId", e.target.value)} required />
              </div>
              <div className="space-y-2">
                <Label>{t("resellerDetail.accrueBasis")}</Label>
                <Input value={form.basis} onChange={(e) => set("basis", e.target.value)} inputMode="numeric" required />
                <p className="text-xs text-muted-foreground">{t("resellerDetail.accrueBasisHint")}</p>
              </div>
              <div className="space-y-2">
                <Label>{t("resellerDetail.accrueRate")}</Label>
                <Input value={form.rate} onChange={(e) => set("rate", e.target.value)} inputMode="decimal" />
                <p className="text-xs text-muted-foreground">{t("resellerDetail.accrueRateHint")}</p>
              </div>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={accrueMut.isPending}>
              {accrueMut.isPending ? t("common.loading") : t("resellerDetail.accrue")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// ─── Wholesale price overrides ───

function PricesCard({ resellerId }: { resellerId: string }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "resellers", resellerId, "prices"],
    queryFn: () => admin.listPriceOverrides(resellerId),
  })
  // Plan names for the listing — an id column alone would make the
  // price list unreadable. Falls back to the id when a plan is not in
  // the first page of the lookup.
  const { data: plansData } = useQuery({
    queryKey: ["admin", "plans", "names"],
    queryFn: () => admin.listPlans({ limit: 200 }),
  })
  const planName = (planId: string) => plansData?.plans?.find((p) => p.id === planId)?.name || planId

  const [editing, setEditing] = useState<ResellerPriceOverride | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<ResellerPriceOverride | null>(null)

  const prices = data?.prices || []

  const deleteMut = useMutation({
    mutationFn: (planId: string) => admin.deletePriceOverride(resellerId, planId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "resellers", resellerId, "prices"] })
      showToast(t("toast.priceDeleted"), "success")
      setDeleting(null)
    },
    onError: (e: Error) => toastError(e),
  })

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0">
        <CardTitle className="text-base">{t("resellerDetail.prices")}</CardTitle>
        <Button size="sm" variant="outline" onClick={() => setCreating(true)}>
          <Plus className="h-4 w-4 mr-2" /> {t("resellerDetail.setPrice")}
        </Button>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="h-24 animate-pulse bg-muted rounded-lg" />
        ) : (
          <DataTable>
            <DataTableHeader>
              <DataTableRow>
                <DataTableHead>{t("resellerDetail.colPlan")}</DataTableHead>
                <DataTableHead>{t("resellerDetail.colUnitAmount")}</DataTableHead>
                <DataTableHead>{t("resellerDetail.colCurrency")}</DataTableHead>
                <DataTableHead>{t("common.updated")}</DataTableHead>
                <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
              </DataTableRow>
            </DataTableHeader>
            <DataTableBody>
              {prices.length === 0 && <DataTableEmpty colSpan={5} message={t("resellerDetail.pricesEmpty")} />}
              {prices.map((p: ResellerPriceOverride) => (
                <DataTableRow key={p.plan_id}>
                  <DataTableCell className="font-medium">{planName(p.plan_id)}</DataTableCell>
                  <DataTableCell>{formatMinor(p.unit_amount_minor, p.currency)}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">{p.currency}</DataTableCell>
                  <DataTableCell className="text-muted-foreground">{formatDate(p.updated_at)}</DataTableCell>
                  <DataTableCell>
                    <div className="flex justify-end gap-1">
                      <Button variant="ghost" size="icon" onClick={() => setEditing(p)}>
                        <Banknote className="h-4 w-4" />
                      </Button>
                      <Button variant="ghost" size="icon" onClick={() => setDeleting(p)}>
                        <Trash2 className="h-4 w-4 text-destructive" />
                      </Button>
                    </div>
                  </DataTableCell>
                </DataTableRow>
              ))}
            </DataTableBody>
          </DataTable>
        )}
      </CardContent>

      {(creating || editing) && (
        <PriceDialog
          resellerId={resellerId}
          price={editing || undefined}
          onClose={() => {
            setCreating(false)
            setEditing(null)
          }}
        />
      )}

      <AlertDialog open={!!deleting} onOpenChange={() => setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("resellerDetail.priceDeleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("resellerDetail.priceDeleteDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => deleting && deleteMut.mutate(deleting.plan_id)}
            >
              {t("common.delete")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}

function PriceDialog({
  resellerId,
  price,
  onClose,
}: {
  resellerId: string
  price?: ResellerPriceOverride
  onClose: () => void
}) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [planId, setPlanId] = useState(price?.plan_id || "")
  const [search, setSearch] = useState("")
  const [unitMinor, setUnitMinor] = useState(price ? String(price.unit_amount_minor) : "")
  const [currency, setCurrency] = useState(price?.currency || "")

  const { data, isFetching } = useQuery({
    queryKey: ["admin", "plans", "picker", search],
    queryFn: () => admin.listPlans({ search, limit: PICKER_PAGE }),
    placeholderData: keepPreviousData,
    enabled: !price,
  })
  const plans = data?.plans || []

  const saveMut = useMutation({
    mutationFn: (body: { unit_amount_minor: number; currency: string }) =>
      admin.setPriceOverride(resellerId, planId, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "resellers", resellerId, "prices"] })
      showToast(t("toast.priceSaved"), "success")
      onClose()
    },
    onError: (e: Error) => toastError(e),
  })

  const submit = () => {
    if (!planId) return showToast(t("resellerDetail.errPlan"), "error")
    const unit = parseIntStrict(unitMinor)
    if (unit === null) return showToast(t("resellerDetail.errUnitAmount"), "error")
    const cur = currency.trim().toUpperCase()
    if (!/^[A-Z]{3}$/.test(cur)) return showToast(t("resellerDetail.errCurrency"), "error")
    saveMut.mutate({ unit_amount_minor: unit, currency: cur })
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{price ? t("resellerDetail.editPrice") : t("resellerDetail.setPrice")}</DialogTitle>
          <DialogDescription>{t("resellerDetail.priceDesc")}</DialogDescription>
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
              <div className="space-y-2 sm:col-span-2">
                <Label>{t("resellerDetail.colPlan")}</Label>
                {price ? (
                  // The (reseller, plan) pair is the whole identity — an
                  // override's plan cannot be retargeted, only rewritten.
                  <Input value={price.plan_id} disabled />
                ) : (
                  <SearchableSelect
                    value={planId}
                    onChange={setPlanId}
                    options={plans.map((p) => ({ id: p.id, label: p.name, hint: p.license_type || undefined }))}
                    total={data?.total || 0}
                    search={search}
                    onSearchChange={setSearch}
                    current={null}
                    searchPlaceholder={t("filter.searchPlans")}
                    emptyLabel={t("filter.noMatches")}
                    moreLabel={(hidden) => t("filter.moreMatches", { count: hidden })}
                    className="w-full"
                    loading={isFetching}
                  />
                )}
              </div>
              <div className="space-y-2">
                <Label>{t("resellerDetail.colUnitAmount")}</Label>
                <Input value={unitMinor} onChange={(e) => setUnitMinor(e.target.value)} inputMode="numeric" required />
                <p className="text-xs text-muted-foreground">{t("resellerDetail.unitAmountHint")}</p>
              </div>
              <div className="space-y-2">
                <Label>{t("resellerDetail.colCurrency")}</Label>
                <Input
                  value={currency}
                  onChange={(e) => setCurrency(e.target.value)}
                  placeholder="USD"
                  maxLength={3}
                  required
                />
              </div>
            </div>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={saveMut.isPending}>
              {saveMut.isPending ? t("common.loading") : t("common.save")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
