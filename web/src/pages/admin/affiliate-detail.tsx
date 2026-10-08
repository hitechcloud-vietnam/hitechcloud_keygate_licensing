import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { ArrowLeft, Check, Pencil, Plus, RotateCcw, Trash2, X } from "lucide-react"
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
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { type TranslationKeys, useI18n } from "@/i18n"
import {
  type Affiliate,
  type AffiliateConversion,
  type AffiliatePayout,
  ApiError,
  admin,
  type ReferralCode,
} from "@/lib/api"
import { errorMessage } from "@/lib/errors"
import { bpsToPercentString, formatMinorUnits, parseIntStrict, percentStringToBps } from "@/lib/money"
import { boolColor, formatDate, statusColor } from "@/lib/utils"
import { ledgerBadgeColor } from "@/pages/admin/resellers"

const AFFILIATE_STATUSES = ["active", "suspended"] as const
const COMMISSION_MODELS = ["percent", "fixed"] as const
const CONVERSION_STATUSES = ["pending", "approved", "paid", "rejected", "reversed"] as const

export default function AffiliateDetailPage() {
  const { t } = useI18n()
  const { id = "" } = useParams()
  const { data, isLoading, error } = useQuery({
    queryKey: ["admin", "affiliates", id],
    queryFn: () => admin.getAffiliate(id),
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

  const affiliate = data?.affiliate
  if (!affiliate) {
    return (
      <div className="space-y-6">
        <Link
          to="/admin/affiliates"
          className="inline-flex items-center gap-2 text-sm text-muted-foreground hover:underline"
        >
          <ArrowLeft className="h-4 w-4" /> {t("affiliateDetail.backToList")}
        </Link>
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">
            {error ? (error instanceof Error ? errorMessage(error) : String(error)) : t("affiliateDetail.notFound")}
          </CardContent>
        </Card>
      </div>
    )
  }

  return (
    <div className="space-y-6">
      <Link
        to="/admin/affiliates"
        className="inline-flex items-center gap-2 text-sm text-muted-foreground hover:underline"
      >
        <ArrowLeft className="h-4 w-4" /> {t("affiliateDetail.backToList")}
      </Link>

      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <div className="flex items-center gap-3">
            <h1 className="text-2xl font-bold tracking-tight">{affiliate.name}</h1>
            <Badge className={statusColor(affiliate.status)}>
              {t(`status.${affiliate.status}` as TranslationKeys)}
            </Badge>
          </div>
          <p className="text-muted-foreground">{affiliate.contact_email}</p>
        </div>
        <div className="text-right">
          <p className="text-xs text-muted-foreground">{t("affiliateDetail.pendingCommission")}</p>
          <p className="text-xl font-semibold">
            {formatMinorUnits(data?.pending_commission_minor ?? 0)}{" "}
            <span className="text-xs font-normal text-muted-foreground">{t("common.minorUnits")}</span>
          </p>
        </div>
      </div>

      <ProfileForm key={affiliate.updated_at} affiliate={affiliate} />
      <CodesCard affiliateId={affiliate.id} />
      <ConversionsCard affiliateId={affiliate.id} />
      <PayoutsCard affiliateId={affiliate.id} />
    </div>
  )
}

// ─── Profile ───

function ProfileForm({ affiliate }: { affiliate: Affiliate }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [form, setForm] = useState({
    name: affiliate.name,
    contactEmail: affiliate.contact_email,
    status: affiliate.status,
    commissionModel: affiliate.commission_model,
    rate: bpsToPercentString(affiliate.commission_bps),
    fixedMinor: String(affiliate.commission_minor),
    payoutMethod: affiliate.payout_method,
    notes: affiliate.notes,
  })
  const set = (k: string, v: string) => setForm((f) => ({ ...f, [k]: v }))

  const saveMut = useMutation({
    mutationFn: (body: {
      name: string
      contact_email: string
      status: string
      commission_model: string
      commission_bps: number
      commission_minor: number
      payout_method: string
      notes: string
    }) => admin.updateAffiliate(affiliate.id, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "affiliates"] })
      showToast(t("toast.affiliateSaved"), "success")
    },
    onError: (e: Error) => toastError(e),
  })

  const submit = () => {
    const name = form.name.trim()
    const contactEmail = form.contactEmail.trim()
    if (!name) return showToast(t("affiliates.errName"), "error")
    if (!contactEmail.includes("@")) return showToast(t("affiliates.errEmail"), "error")
    // Both commission numbers travel whatever the model — the ledger
    // stores both so switching models is an edit, not a migration.
    const bps = percentStringToBps(form.rate)
    if (bps === null || bps > 10000) return showToast(t("affiliates.errRate"), "error")
    const fixed = parseIntStrict(form.fixedMinor)
    if (fixed === null) return showToast(t("affiliates.errFixed"), "error")
    saveMut.mutate({
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
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t("affiliateDetail.profile")}</CardTitle>
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
            <div className="space-y-2">
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

// ─── Referral codes ───

function CodesCard({ affiliateId }: { affiliateId: string }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const pg = useServerPagination(5, [])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "affiliates", affiliateId, "codes", pg.page, pg.pageSize],
    queryFn: () => admin.listReferralCodes(affiliateId, pg.params),
  })
  const [editing, setEditing] = useState<ReferralCode | null>(null)
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<ReferralCode | null>(null)

  const { items: codes, total, totalPages } = pg.from(data, data?.codes)

  const deleteMut = useMutation({
    mutationFn: (codeId: string) => admin.deleteReferralCode(affiliateId, codeId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "affiliates", affiliateId, "codes"] })
      showToast(t("toast.codeDeleted"), "success")
      setDeleting(null)
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.code === "REFERRAL_CODE_HAS_CONVERSIONS") {
        toastError(e, t("affiliateDetail.codeDeleteBlocked"))
      } else {
        toastError(e)
      }
    },
  })

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0">
        <CardTitle className="text-base">{t("affiliateDetail.codes")}</CardTitle>
        <Button size="sm" variant="outline" onClick={() => setCreating(true)}>
          <Plus className="h-4 w-4 mr-2" /> {t("affiliateDetail.newCode")}
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
                  <DataTableHead>{t("affiliateDetail.colCode")}</DataTableHead>
                  <DataTableHead>{t("affiliateDetail.colLandingUrl")}</DataTableHead>
                  <DataTableHead>{t("common.active")}</DataTableHead>
                  <DataTableHead>{t("common.created")}</DataTableHead>
                  <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
                </DataTableRow>
              </DataTableHeader>
              <DataTableBody>
                {codes.length === 0 && <DataTableEmpty colSpan={5} message={t("affiliateDetail.codesEmpty")} />}
                {codes.map((rc: ReferralCode) => (
                  <DataTableRow key={rc.id}>
                    <DataTableCell className="font-medium">
                      <CopyableId id={rc.code} />
                    </DataTableCell>
                    <DataTableCell className="max-w-64 truncate text-muted-foreground">
                      {rc.landing_url || "-"}
                    </DataTableCell>
                    <DataTableCell>
                      <Badge className={boolColor(rc.active)}>
                        {rc.active ? t("common.active") : t("common.inactive")}
                      </Badge>
                    </DataTableCell>
                    <DataTableCell className="text-muted-foreground">{formatDate(rc.created_at)}</DataTableCell>
                    <DataTableCell>
                      <div className="flex justify-end gap-1">
                        <Button variant="ghost" size="icon" onClick={() => setEditing(rc)}>
                          <Pencil className="h-4 w-4" />
                        </Button>
                        <Button variant="ghost" size="icon" onClick={() => setDeleting(rc)}>
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

      {(creating || editing) && (
        <CodeDialog
          affiliateId={affiliateId}
          code={editing || undefined}
          onClose={() => {
            setCreating(false)
            setEditing(null)
          }}
        />
      )}

      <AlertDialog open={!!deleting} onOpenChange={() => setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("affiliateDetail.codeDeleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("affiliateDetail.codeDeleteDesc")}</AlertDialogDescription>
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
    </Card>
  )
}

function CodeDialog({ affiliateId, code, onClose }: { affiliateId: string; code?: ReferralCode; onClose: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [form, setForm] = useState({
    handle: code?.code || "",
    landingUrl: code?.landing_url || "",
    active: code?.active ?? true,
  })

  const saveMut = useMutation({
    mutationFn: (body: { landing_url?: string; active?: boolean }) =>
      code
        ? admin.updateReferralCode(affiliateId, code.id, body)
        : admin.createReferralCode(affiliateId, { code: form.handle.trim(), ...body }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "affiliates", affiliateId, "codes"] })
      showToast(code ? t("toast.codeSaved") : t("toast.codeCreated"), "success")
      onClose()
    },
    onError: (e: Error) => toastError(e),
  })

  const submit = () => {
    const handle = form.handle.trim()
    // The server folds the handle (uppercase alphanumerics) and
    // refuses anything outside 4..32 — checked here so the error names
    // the field the admin typed in.
    if (!code && !/^[A-Za-z0-9]{4,32}$/.test(handle)) return showToast(t("affiliateDetail.errCode"), "error")
    const landing = form.landingUrl.trim()
    if (landing && !/^https?:\/\/\S+$/i.test(landing)) return showToast(t("affiliateDetail.errLandingUrl"), "error")
    saveMut.mutate({ landing_url: landing, active: form.active })
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{code ? t("affiliateDetail.editCode") : t("affiliateDetail.newCode")}</DialogTitle>
          <DialogDescription>{t("affiliateDetail.codeDesc")}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
          className="flex min-h-0 flex-1 flex-col gap-4"
        >
          <DialogBody className="space-y-4">
            <div className="grid grid-cols-1 gap-4">
              <div className="space-y-2">
                <Label>{t("affiliateDetail.colCode")}</Label>
                {/* A shared link's handle is immutable — renaming one
                    would orphan every copy already in the wild. */}
                <Input
                  value={form.handle}
                  onChange={(e) => setForm((f) => ({ ...f, handle: e.target.value }))}
                  disabled={!!code}
                  required={!code}
                />
                <p className="text-xs text-muted-foreground">{t("affiliateDetail.codeHint")}</p>
              </div>
              <div className="space-y-2">
                <Label>{t("affiliateDetail.landingUrl")}</Label>
                <Input
                  value={form.landingUrl}
                  onChange={(e) => setForm((f) => ({ ...f, landingUrl: e.target.value }))}
                  placeholder="https://example.com/pricing"
                />
                <p className="text-xs text-muted-foreground">{t("affiliateDetail.landingUrlHint")}</p>
              </div>
              <div className="flex items-center gap-3">
                <input
                  type="checkbox"
                  id="code-active"
                  checked={form.active}
                  onChange={(e) => setForm((f) => ({ ...f, active: e.target.checked }))}
                  className="h-4 w-4 rounded border-input accent-primary"
                />
                <Label htmlFor="code-active" className="font-normal">
                  {t("common.active")}
                </Label>
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

// ─── Conversions ───

type ConversionAction = "approve" | "reject" | "reverse"

function ConversionsCard({ affiliateId }: { affiliateId: string }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [statusFilter, setStatusFilter] = useState("")
  const pg = useServerPagination(5, [statusFilter])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "affiliates", affiliateId, "conversions", statusFilter, pg.page, pg.pageSize],
    queryFn: () => admin.listAffiliateConversions(affiliateId, { status: statusFilter || undefined, ...pg.params }),
  })
  const [pendingAction, setPendingAction] = useState<{ conv: AffiliateConversion; action: ConversionAction } | null>(
    null,
  )

  const { items: conversions, total, totalPages } = pg.from(data, data?.conversions)

  // The three review decisions share one state machine server-side:
  // an illegal move is 409 CONVERSION_TRANSITION_INVALID and a
  // conversion a pending payout has claimed is frozen
  // (CONVERSION_IN_PAYOUT) — both answered with text that says what to
  // do instead of a bare code.
  const actionMut = useMutation({
    mutationFn: ({ conv, action }: { conv: AffiliateConversion; action: ConversionAction }) =>
      action === "approve"
        ? admin.approveConversion(conv.id)
        : action === "reject"
          ? admin.rejectConversion(conv.id)
          : admin.reverseConversion(conv.id),
    onSuccess: (_res, vars) => {
      qc.invalidateQueries({ queryKey: ["admin", "affiliates", affiliateId, "conversions"] })
      qc.invalidateQueries({ queryKey: ["admin", "affiliates", affiliateId] })
      showToast(
        vars.action === "approve"
          ? t("toast.conversionApproved")
          : vars.action === "reject"
            ? t("toast.conversionRejected")
            : t("toast.conversionReversed"),
        "success",
      )
      setPendingAction(null)
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.code === "CONVERSION_TRANSITION_INVALID") {
        toastError(e, t("affiliateDetail.errConversionTransition"))
      } else if (e instanceof ApiError && e.code === "CONVERSION_IN_PAYOUT") {
        toastError(e, t("affiliateDetail.errConversionInPayout"))
      } else {
        toastError(e)
      }
    },
  })

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0 gap-3">
        <CardTitle className="text-base">{t("affiliateDetail.conversions")}</CardTitle>
        <Select value={statusFilter} onValueChange={(v) => setStatusFilter(v === "all" ? "" : v)}>
          <SelectTrigger className="w-40">
            <SelectValue placeholder={t("filter.allStatuses")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("filter.allStatuses")}</SelectItem>
            {CONVERSION_STATUSES.map((s) => (
              <SelectItem key={s} value={s}>
                {t(`status.${s}` as TranslationKeys)}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>
      </CardHeader>
      <CardContent>
        {isLoading ? (
          <div className="h-24 animate-pulse bg-muted rounded-lg" />
        ) : (
          <>
            <DataTable>
              <DataTableHeader>
                <DataTableRow>
                  <DataTableHead>{t("affiliateDetail.colOrder")}</DataTableHead>
                  <DataTableHead>{t("affiliateDetail.colOrderTotal")}</DataTableHead>
                  <DataTableHead>{t("affiliateDetail.colCommission")}</DataTableHead>
                  <DataTableHead>{t("common.status")}</DataTableHead>
                  <DataTableHead>{t("common.created")}</DataTableHead>
                  <DataTableHead className="w-32 text-right">{t("common.actions")}</DataTableHead>
                </DataTableRow>
              </DataTableHeader>
              <DataTableBody>
                {conversions.length === 0 && (
                  <DataTableEmpty colSpan={6} message={t("affiliateDetail.conversionsEmpty")} />
                )}
                {conversions.map((conv: AffiliateConversion) => (
                  <DataTableRow key={conv.id}>
                    <DataTableCell className="font-medium">
                      <CopyableId id={conv.order_id} />
                    </DataTableCell>
                    <DataTableCell>{formatMinorUnits(conv.order_total_minor)}</DataTableCell>
                    <DataTableCell className="font-medium">{formatMinorUnits(conv.commission_minor)}</DataTableCell>
                    <DataTableCell>
                      <Badge className={ledgerBadgeColor(conv.status)}>
                        {t(`status.${conv.status}` as TranslationKeys)}
                      </Badge>
                    </DataTableCell>
                    <DataTableCell className="text-muted-foreground">{formatDate(conv.created_at)}</DataTableCell>
                    <DataTableCell>
                      <div className="flex justify-end gap-1">
                        {conv.status === "pending" && (
                          <>
                            <Button
                              variant="ghost"
                              size="icon"
                              title={t("affiliateDetail.approve")}
                              onClick={() => setPendingAction({ conv, action: "approve" })}
                            >
                              <Check className="h-4 w-4 text-emerald-600" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              title={t("affiliateDetail.reject")}
                              onClick={() => setPendingAction({ conv, action: "reject" })}
                            >
                              <X className="h-4 w-4 text-destructive" />
                            </Button>
                          </>
                        )}
                        {(conv.status === "pending" || conv.status === "approved" || conv.status === "paid") && (
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("affiliateDetail.reverse")}
                            onClick={() => setPendingAction({ conv, action: "reverse" })}
                          >
                            <RotateCcw className="h-4 w-4 text-amber-600" />
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

      <AlertDialog open={!!pendingAction} onOpenChange={() => setPendingAction(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {pendingAction?.action === "approve"
                ? t("affiliateDetail.approveTitle")
                : pendingAction?.action === "reject"
                  ? t("affiliateDetail.rejectTitle")
                  : t("affiliateDetail.reverseTitle")}
            </AlertDialogTitle>
            <AlertDialogDescription>
              {pendingAction?.action === "approve"
                ? t("affiliateDetail.approveDesc")
                : pendingAction?.action === "reject"
                  ? t("affiliateDetail.rejectDesc")
                  : t("affiliateDetail.reverseDesc")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className={
                pendingAction?.action === "approve" ? undefined : "bg-destructive text-white hover:bg-destructive/90"
              }
              onClick={() => pendingAction && actionMut.mutate(pendingAction)}
            >
              {pendingAction?.action === "approve"
                ? t("affiliateDetail.approve")
                : pendingAction?.action === "reject"
                  ? t("affiliateDetail.reject")
                  : t("affiliateDetail.reverse")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </Card>
  )
}

// ─── Payouts ───

function PayoutsCard({ affiliateId }: { affiliateId: string }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const pg = useServerPagination(5, [])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "affiliates", affiliateId, "payouts", pg.page, pg.pageSize],
    queryFn: () => admin.listAffiliatePayouts(affiliateId, pg.params),
  })
  const [creating, setCreating] = useState(false)
  const [paying, setPaying] = useState<AffiliatePayout | null>(null)
  const [failing, setFailing] = useState<AffiliatePayout | null>(null)

  const { items: payouts, total, totalPages } = pg.from(data, data?.payouts)

  // Only a requested payout moves (409 PAYOUT_TRANSITION_INVALID
  // otherwise) — what happened to the money is recorded once.
  const paidMut = useMutation({
    mutationFn: (payoutId: string) => admin.markPayoutPaid(payoutId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "affiliates", affiliateId] })
      showToast(t("toast.payoutPaid"), "success")
      setPaying(null)
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.code === "PAYOUT_TRANSITION_INVALID") {
        toastError(e, t("affiliateDetail.errPayoutTransition"))
      } else {
        toastError(e)
      }
    },
  })

  return (
    <Card>
      <CardHeader className="flex flex-row items-center justify-between space-y-0">
        <CardTitle className="text-base">{t("affiliateDetail.payouts")}</CardTitle>
        <Button size="sm" variant="outline" onClick={() => setCreating(true)}>
          <Plus className="h-4 w-4 mr-2" /> {t("affiliateDetail.newPayout")}
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
                  <DataTableHead>{t("affiliateDetail.colCommission")}</DataTableHead>
                  <DataTableHead>{t("common.status")}</DataTableHead>
                  <DataTableHead>{t("orders.paidAt")}</DataTableHead>
                  <DataTableHead>{t("common.notes")}</DataTableHead>
                  <DataTableHead>{t("common.created")}</DataTableHead>
                  <DataTableHead className="w-32 text-right">{t("common.actions")}</DataTableHead>
                </DataTableRow>
              </DataTableHeader>
              <DataTableBody>
                {payouts.length === 0 && <DataTableEmpty colSpan={6} message={t("affiliateDetail.payoutsEmpty")} />}
                {payouts.map((p: AffiliatePayout) => (
                  <DataTableRow key={p.id}>
                    <DataTableCell className="font-medium">
                      {formatMinorUnits(p.amount_minor)}{" "}
                      <span className="text-xs font-normal text-muted-foreground">{t("common.minorUnits")}</span>
                    </DataTableCell>
                    <DataTableCell>
                      <Badge className={ledgerBadgeColor(p.status)}>{t(`status.${p.status}` as TranslationKeys)}</Badge>
                    </DataTableCell>
                    <DataTableCell className="text-muted-foreground">{formatDate(p.paid_at)}</DataTableCell>
                    <DataTableCell className="max-w-48 truncate text-muted-foreground">{p.notes || "-"}</DataTableCell>
                    <DataTableCell className="text-muted-foreground">{formatDate(p.created_at)}</DataTableCell>
                    <DataTableCell>
                      <div className="flex justify-end gap-1">
                        {p.status === "requested" && (
                          <>
                            <Button
                              variant="ghost"
                              size="icon"
                              title={t("affiliateDetail.markPaid")}
                              onClick={() => setPaying(p)}
                            >
                              <Check className="h-4 w-4 text-emerald-600" />
                            </Button>
                            <Button
                              variant="ghost"
                              size="icon"
                              title={t("affiliateDetail.markFailed")}
                              onClick={() => setFailing(p)}
                            >
                              <X className="h-4 w-4 text-destructive" />
                            </Button>
                          </>
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

      {creating && <PayoutDialog affiliateId={affiliateId} onClose={() => setCreating(false)} />}

      <AlertDialog open={!!paying} onOpenChange={() => setPaying(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("affiliateDetail.markPaidTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("affiliateDetail.markPaidDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction onClick={() => paying && paidMut.mutate(paying.id)}>
              {t("affiliateDetail.markPaid")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>

      {failing && <PayoutFailDialog affiliateId={affiliateId} payout={failing} onClose={() => setFailing(null)} />}
    </Card>
  )
}

function PayoutDialog({ affiliateId, onClose }: { affiliateId: string; onClose: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [amount, setAmount] = useState("")
  const [notes, setNotes] = useState("")

  const createMut = useMutation({
    mutationFn: (body: { amount_minor?: number; notes: string }) => admin.createAffiliatePayout(affiliateId, body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "affiliates", affiliateId] })
      showToast(t("toast.payoutCreated"), "success")
      onClose()
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.code === "PAYOUT_NOTHING_TO_PAY") {
        toastError(e, t("affiliateDetail.errPayoutNothing"))
      } else {
        toastError(e)
      }
    },
  })

  const submit = () => {
    // Blank (or 0) settles the whole accrued balance — the store
    // claims whole conversions oldest-first and records what it
    // actually settled. Everything is integer minor units.
    let amountMinor: number | undefined
    if (amount.trim() !== "") {
      const parsed = parseIntStrict(amount)
      if (parsed === null) return showToast(t("affiliateDetail.errPayoutAmount"), "error")
      amountMinor = parsed
    }
    createMut.mutate({ ...(amountMinor !== undefined ? { amount_minor: amountMinor } : {}), notes: notes.trim() })
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("affiliateDetail.payoutTitle")}</DialogTitle>
          <DialogDescription>{t("affiliateDetail.payoutDesc")}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            submit()
          }}
          className="flex min-h-0 flex-1 flex-col gap-4"
        >
          <DialogBody className="space-y-4">
            <div className="grid grid-cols-1 gap-4">
              <div className="space-y-2">
                <Label>{t("affiliateDetail.payoutAmount")}</Label>
                <Input value={amount} onChange={(e) => setAmount(e.target.value)} inputMode="numeric" />
                <p className="text-xs text-muted-foreground">{t("affiliateDetail.payoutAmountHint")}</p>
              </div>
              <div className="space-y-2">
                <Label>{t("common.notes")}</Label>
                <textarea
                  value={notes}
                  onChange={(e) => setNotes(e.target.value)}
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

function PayoutFailDialog({
  affiliateId,
  payout,
  onClose,
}: {
  affiliateId: string
  payout: AffiliatePayout
  onClose: () => void
}) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [notes, setNotes] = useState("")

  const failMut = useMutation({
    mutationFn: () => admin.markPayoutFailed(payout.id, notes.trim()),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "affiliates", affiliateId] })
      showToast(t("toast.payoutFailed"), "success")
      onClose()
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.code === "PAYOUT_TRANSITION_INVALID") {
        toastError(e, t("affiliateDetail.errPayoutTransition"))
      } else {
        toastError(e)
      }
    },
  })

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("affiliateDetail.markFailedTitle")}</DialogTitle>
          <DialogDescription>{t("affiliateDetail.markFailedDesc")}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            failMut.mutate()
          }}
          className="flex min-h-0 flex-1 flex-col gap-4"
        >
          <DialogBody className="space-y-4">
            <div className="space-y-2">
              <Label>{t("common.notes")}</Label>
              <textarea
                value={notes}
                onChange={(e) => setNotes(e.target.value)}
                rows={2}
                className="flex w-full rounded-md border border-input bg-background px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
              />
            </div>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" variant="destructive" disabled={failMut.isPending}>
              {failMut.isPending ? t("common.loading") : t("affiliateDetail.markFailed")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}
