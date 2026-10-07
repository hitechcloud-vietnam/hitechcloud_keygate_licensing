import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Pencil, Plus, Trash2 } from "lucide-react"
import { useState } from "react"
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
import { useI18n } from "@/i18n"
import { admin, type TaxRate, type TaxRateInput } from "@/lib/api"
import { bpsToPercentString, formatBps, percentStringToBps } from "@/lib/money"
import { boolColor } from "@/lib/utils"

export default function TaxRatesPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [search, setSearch] = useState("")
  const pg = useServerPagination(10, [search])
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "tax-rates", search, pg.page, pg.pageSize],
    queryFn: () => admin.listTaxRates({ search, ...pg.params }),
  })
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<TaxRate | null>(null)
  const [deleting, setDeleting] = useState<TaxRate | null>(null)

  const { items: rates, total, totalPages } = pg.from(data, data?.tax_rates)

  const deleteMut = useMutation({
    mutationFn: (id: string) => admin.deleteTaxRate(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "tax-rates"] })
      showToast(t("toast.taxRateDeleted"), "success")
      setDeleting(null)
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight sr-only md:not-sr-only">{t("taxRates.title")}</h1>
          <p className="text-muted-foreground">{t("taxRates.subtitle")}</p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="h-4 w-4 mr-2" /> {t("taxRates.new")}
        </Button>
      </div>

      <div className="flex flex-wrap gap-3">
        <Input
          placeholder={t("common.search")}
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
                    <DataTableHead>{t("taxRates.jurisdiction")}</DataTableHead>
                    <DataTableHead>{t("taxRates.colRate")}</DataTableHead>
                    <DataTableHead>{t("taxRates.inclusive")}</DataTableHead>
                    <DataTableHead>{t("taxRates.country")}</DataTableHead>
                    <DataTableHead>{t("taxRates.region")}</DataTableHead>
                    <DataTableHead>{t("common.status")}</DataTableHead>
                    <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {rates.length === 0 && <DataTableEmpty colSpan={7} message={t("taxRates.empty")} />}
                  {rates.map((r: TaxRate) => (
                    <DataTableRow key={r.id}>
                      <DataTableCell className="font-medium">{r.jurisdiction}</DataTableCell>
                      <DataTableCell>{formatBps(r.basis_points)}</DataTableCell>
                      <DataTableCell>
                        <Badge variant="secondary">{r.inclusive ? t("common.yes") : t("common.no")}</Badge>
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">{r.country || "-"}</DataTableCell>
                      <DataTableCell className="text-muted-foreground">{r.region || "-"}</DataTableCell>
                      <DataTableCell>
                        <Badge className={boolColor(r.active)}>
                          {r.active ? t("common.active") : t("common.inactive")}
                        </Badge>
                      </DataTableCell>
                      <DataTableCell>
                        <div className="flex justify-end gap-1">
                          <Button variant="ghost" size="icon" onClick={() => setEditing(r)}>
                            <Pencil className="h-4 w-4" />
                          </Button>
                          <Button variant="ghost" size="icon" onClick={() => setDeleting(r)}>
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

      {creating && <TaxRateDialog open onClose={() => setCreating(false)} />}
      {editing && <TaxRateDialog open onClose={() => setEditing(null)} rate={editing} />}

      <AlertDialog open={!!deleting} onOpenChange={() => setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>
              {t("common.delete")} "{deleting?.jurisdiction}"?
            </AlertDialogTitle>
            <AlertDialogDescription>{t("taxRates.deleteConfirm")}</AlertDialogDescription>
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

function TaxRateDialog({ open, onClose, rate }: { open: boolean; onClose: () => void; rate?: TaxRate }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [form, setForm] = useState({
    jurisdiction: rate?.jurisdiction || "",
    // Typed as a percent, stored as basis points — the conversion is
    // exact both ways (see lib/money), so no rate is lost to floats.
    percent: rate ? bpsToPercentString(rate.basis_points) : "",
    country: rate?.country || "",
    region: rate?.region || "",
    description: rate?.description || "",
    inclusive: rate?.inclusive ?? false,
    active: rate?.active ?? true,
  })

  const set = (k: string, v: string | boolean) => setForm((f) => ({ ...f, [k]: v }))

  const createMut = useMutation({
    mutationFn: (body: TaxRateInput) => (rate ? admin.updateTaxRate(rate.id, body) : admin.createTaxRate(body)),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["admin", "tax-rates"] })
      showToast(rate ? t("toast.taxRateSaved") : t("toast.taxRateCreated"), "success")
      onClose()
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  const submit = () => {
    const jurisdiction = form.jurisdiction.trim()
    if (!jurisdiction || jurisdiction.length > 64) return showToast(t("taxRates.errJurisdiction"), "error")
    const bps = percentStringToBps(form.percent)
    // The table caps a rate at 1000% — the typo guard on the server.
    if (bps === null || bps > 100_000) return showToast(t("taxRates.errRate"), "error")

    const body: TaxRateInput = {
      jurisdiction,
      basis_points: bps,
      inclusive: form.inclusive,
      country: form.country.trim(),
      region: form.region.trim(),
      description: form.description,
    }
    // Creation always starts an active rate (the server says so); the
    // switch only appears on an existing one, where PATCH carries it.
    if (rate) body.active = form.active
    createMut.mutate(body)
  }

  return (
    <Dialog open={open} onOpenChange={onClose}>
      <DialogContent className="max-w-lg h-[min(640px,85vh)]">
        <DialogHeader>
          <DialogTitle>{rate ? t("taxRates.edit") : t("taxRates.new")}</DialogTitle>
          <DialogDescription>{t("taxRates.formDesc")}</DialogDescription>
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
                <Label>{t("taxRates.jurisdiction")}</Label>
                <Input
                  value={form.jurisdiction}
                  onChange={(e) => set("jurisdiction", e.target.value)}
                  placeholder="US-CA"
                  required
                />
                <p className="text-xs text-muted-foreground">{t("taxRates.jurisdictionHint")}</p>
              </div>
              <div className="space-y-2">
                <Label>{t("taxRates.rate")}</Label>
                <Input
                  value={form.percent}
                  onChange={(e) => set("percent", e.target.value)}
                  inputMode="decimal"
                  required
                />
                <p className="text-xs text-muted-foreground">{t("taxRates.rateHint")}</p>
              </div>
              <div className="space-y-2">
                <Label>{t("taxRates.description")}</Label>
                <Input value={form.description} onChange={(e) => set("description", e.target.value)} />
              </div>
              <div className="space-y-2">
                <Label>{t("taxRates.country")}</Label>
                <Input value={form.country} onChange={(e) => set("country", e.target.value)} placeholder="US" />
              </div>
              <div className="space-y-2">
                <Label>{t("taxRates.region")}</Label>
                <Input value={form.region} onChange={(e) => set("region", e.target.value)} placeholder="CA" />
              </div>
              <div className="flex items-center gap-3">
                <input
                  type="checkbox"
                  id="tax-inclusive"
                  checked={form.inclusive}
                  onChange={(e) => set("inclusive", e.target.checked)}
                  className="h-4 w-4 rounded border-input accent-primary"
                />
                <Label htmlFor="tax-inclusive" className="font-normal">
                  {t("taxRates.inclusive")}
                </Label>
              </div>
              {rate && (
                <div className="flex items-center gap-3">
                  <input
                    type="checkbox"
                    id="tax-active"
                    checked={form.active}
                    onChange={(e) => set("active", e.target.checked)}
                    className="h-4 w-4 rounded border-input accent-primary"
                  />
                  <Label htmlFor="tax-active" className="font-normal">
                    {t("common.active")}
                  </Label>
                </div>
              )}
              <p className="text-xs text-muted-foreground sm:col-span-2">{t("taxRates.inclusiveHint")}</p>
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
