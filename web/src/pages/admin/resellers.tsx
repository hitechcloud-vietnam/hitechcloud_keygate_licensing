import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Eye, Handshake, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
import { Link, useNavigate } from "react-router-dom"
import { ListEmptyState } from "@/components/empty-state"
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
import { ApiError, admin, type Reseller } from "@/lib/api"
import { formatBps, percentStringToBps } from "@/lib/money"
import { formatDate, statusColor } from "@/lib/utils"

const RESELLER_STATUSES = ["active", "suspended"] as const

// ledgerBadgeColor colors the partner-ledger vocabularies (reseller
// and affiliate statuses, commission/conversion/payout lifecycles) on
// one palette, exported for the detail pages. Money that is on its way
// out reads differently from money that is settled or clawed back.
export function ledgerBadgeColor(status: string): string {
  switch (status) {
    case "active":
    case "paid":
      return "bg-emerald-100 text-emerald-800"
    case "suspended":
    case "uncollectible":
      return "bg-orange-100 text-orange-800"
    case "pending":
    case "requested":
    case "accrued":
      return "bg-amber-100 text-amber-800"
    case "approved":
    case "open":
      return "bg-blue-100 text-blue-800"
    case "failed":
    case "rejected":
    case "reversed":
    case "refunded":
      return "bg-red-100 text-red-700"
    default:
      return "bg-gray-100 text-gray-800"
  }
}

export default function ResellersPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const navigate = useNavigate()
  const [search, setSearch] = useState("")
  const [statusFilter, setStatusFilter] = useState("")
  const pg = useServerPagination(10, [search, statusFilter])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "resellers", search, statusFilter, pg.page, pg.pageSize],
    queryFn: () => admin.listResellers({ search, status: statusFilter || undefined, ...pg.params }),
  })
  const [creating, setCreating] = useState(false)
  const [deleting, setDeleting] = useState<Reseller | null>(null)

  const { items: resellers, total, totalPages } = pg.from(data, data?.resellers)

  // The documented delete refusals name what is still attached, so the
  // admin knows what to detach instead of guessing from a generic 409.
  const deleteMut = useMutation({
    mutationFn: (id: string) => admin.deleteReseller(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "resellers"] })
      showToast(t("toast.resellerDeleted"), "success")
      setDeleting(null)
    },
    onError: (e: Error) => {
      // The documented delete refusals say what is still attached and
      // what to do about it — keep them, and carry the request id on
      // every refusal for the support reference.
      if (e instanceof ApiError && e.code === "RESSELLER_HAS_ALLOCATIONS") {
        toastError(e, t("resellers.deleteBlockedAllocations"))
      } else if (e instanceof ApiError && e.code === "RESSELLER_HAS_COMMISSIONS") {
        toastError(e, t("resellers.deleteBlockedCommissions"))
      } else {
        toastError(e)
      }
    },
  })

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight sr-only md:not-sr-only">{t("resellers.title")}</h1>
          <p className="text-muted-foreground">{t("resellers.subtitle")}</p>
        </div>
        <div className="flex items-center gap-2">
          <ExportCsvButton
            filename="resellers"
            columns={[
              t("common.name"),
              t("resellers.contactEmail"),
              t("common.status"),
              t("resellers.commissionRate"),
              t("common.created"),
            ]}
            rows={resellers.map((r) => [
              r.name,
              r.contact_email,
              r.status,
              formatBps(r.commission_bps),
              formatDate(r.created_at),
            ])}
          />
          <Button onClick={() => setCreating(true)}>
            <Plus className="h-4 w-4 mr-2" /> {t("resellers.new")}
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
            {RESELLER_STATUSES.map((s) => (
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
          ) : resellers.length === 0 ? (
            <ListEmptyState
              icon={Handshake}
              title={t("resellers.empty")}
              description={t("empty.resellers.desc")}
              action={{ label: t("resellers.new"), onClick: () => setCreating(true) }}
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
                    <DataTableHead>{t("common.name")}</DataTableHead>
                    <DataTableHead>{t("resellers.contactEmail")}</DataTableHead>
                    <DataTableHead>{t("common.status")}</DataTableHead>
                    <DataTableHead>{t("resellers.commissionRate")}</DataTableHead>
                    <DataTableHead>{t("common.created")}</DataTableHead>
                    <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {resellers.map((r: Reseller) => (
                    <DataTableRow key={r.id}>
                      <DataTableCell>
                        <Link to={`/admin/resellers/${r.id}`} className="font-medium hover:underline">
                          {r.name}
                        </Link>
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">{r.contact_email}</DataTableCell>
                      <DataTableCell>
                        <Badge className={statusColor(r.status)}>{t(`status.${r.status}` as TranslationKeys)}</Badge>
                      </DataTableCell>
                      <DataTableCell>{formatBps(r.commission_bps)}</DataTableCell>
                      <DataTableCell className="text-muted-foreground">{formatDate(r.created_at)}</DataTableCell>
                      <DataTableCell>
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("resellers.detail")}
                            aria-label={t("resellers.detail")}
                            onClick={() => navigate(`/admin/resellers/${r.id}`)}
                          >
                            <Eye className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            title={t("common.delete")}
                            aria-label={t("common.delete")}
                            onClick={() => setDeleting(r)}
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

      {creating && <ResellerCreateDialog open onClose={() => setCreating(false)} />}

      <AlertDialog open={!!deleting} onOpenChange={() => setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("common.delete")} "{deleting?.name}"?
            </AlertDialogTitle>
            <AlertDialogDescription>{t("resellers.deleteConfirm")}</AlertDialogDescription>
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

function ResellerCreateDialog({ open, onClose }: { open: boolean; onClose: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [form, setForm] = useState({ name: "", contactEmail: "", status: "active", rate: "0", notes: "" })
  const set = (k: string, v: string) => setForm((f) => ({ ...f, [k]: v }))

  const createMut = useMutation({
    mutationFn: (body: {
      name: string
      contact_email: string
      status: string
      commission_bps: number
      notes: string
    }) => admin.createReseller(body),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "resellers"] })
      showToast(t("toast.resellerCreated"), "success")
      onClose()
    },
    onError: (e: Error) => toastError(e),
  })

  const submit = () => {
    const name = form.name.trim()
    const contactEmail = form.contactEmail.trim()
    if (!name) return showToast(t("resellers.errName"), "error")
    if (!contactEmail.includes("@")) return showToast(t("resellers.errEmail"), "error")
    // The rate is typed as a percent and travels as integer basis
    // points — no float ever touches it (lib/money).
    const bps = percentStringToBps(form.rate)
    if (bps === null || bps > 10000) return showToast(t("resellers.errRate"), "error")
    createMut.mutate({
      name,
      contact_email: contactEmail,
      status: form.status,
      commission_bps: bps,
      notes: form.notes.trim(),
    })
  }

  return (
    <Dialog open={open} onOpenChange={onClose}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{t("resellers.createTitle")}</DialogTitle>
          <DialogDescription>{t("resellers.formDesc")}</DialogDescription>
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
