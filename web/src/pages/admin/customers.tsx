import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Download, Eye, Search, UserX } from "lucide-react"
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
  DataTableSortHead,
  useServerPagination,
  useServerSort,
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
import { Tabs, TabsContent, TabsList, TabsTrigger } from "@/components/ui/tabs"
import { type TranslationKeys, useI18n } from "@/i18n"
import type { AccountAnonymizeResult, User, UserDetail } from "@/lib/api"
import { admin } from "@/lib/api"
import { formatDate, statusColor } from "@/lib/utils"

// downloadJSON hands an export to the browser as a file — the same
// shape as the portal privacy page (see pages/portal/account-privacy):
// a transient object URL, a synthetic anchor click, then release.
function downloadJSON(filename: string, data: unknown): void {
  const blob = new Blob([JSON.stringify(data, null, 2)], { type: "application/json" })
  const url = URL.createObjectURL(blob)
  const a = document.createElement("a")
  a.href = url
  a.download = filename.endsWith(".json") ? filename : `${filename}.json`
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

export default function CustomersPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  // ?search= pre-fills the box: global search (and the command
  // palette) deep-link to this list narrowed to the hit they found.
  const [search, setSearch] = useState(() => new URLSearchParams(window.location.search).get("search") || "")
  // Server-side column sorting (plan §70); the sort state joins the
  // filters so a re-sort lands on the first page of the new order.
  const srt = useServerSort("created_at", "desc")
  const pg = useServerPagination(30, [search, srt.sort, srt.order])
  const [viewingUser, setViewingUser] = useState<string | null>(null)
  // Privacy actions (plan §82), for any user: export downloads the
  // same JSON document the user can pull from their own privacy page;
  // anonymize is the danger action, behind a confirmation.
  const [anonymizing, setAnonymizing] = useState<User | null>(null)
  const [anonymizeResult, setAnonymizeResult] = useState<AccountAnonymizeResult | null>(null)

  const { data, isLoading } = useQuery({
    queryKey: ["admin", "users", search, srt.sort, srt.order, pg.page, pg.pageSize],
    queryFn: () => admin.listUsers({ search: search || undefined, ...srt.params, ...pg.params }),
  })

  const { items: customers, total, totalPages } = pg.from(data, data?.users)

  const exportMut = useMutation({
    mutationFn: (id: string) => admin.exportUser(id),
    onSuccess: (data) => {
      downloadJSON(`user-export-${new Date().toISOString().slice(0, 10)}`, data)
      showToast(t("customers.exportDone"), "success")
    },
    onError: (e: Error) => toastError(e),
  })

  const anonymizeMut = useMutation({
    mutationFn: (id: string) => admin.anonymizeUser(id),
    onSuccess: (result) => {
      qc.invalidateQueries({ queryKey: ["admin", "users"] })
      qc.invalidateQueries({ queryKey: ["admin", "user-detail"] })
      setAnonymizing(null)
      setAnonymizeResult(result)
      showToast(t("customers.anonymizeDone"), "success")
    },
    onError: (e: Error) => toastError(e),
  })

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight sr-only md:not-sr-only">{t("customers.title")}</h1>
        <p className="text-muted-foreground">{t("customers.subtitle", { count: total })}</p>
      </div>

      <div className="flex items-center gap-4">
        <div className="relative flex-1 max-w-sm">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("common.search")}
            aria-label={t("common.search")}
            value={search}
            onChange={(e) => setSearch(e.target.value)}
            className="pl-9"
          />
        </div>
        <ExportCsvButton
          filename="customers"
          columns={[t("customers.customer"), t("common.email"), t("customers.joined"), t("customers.lastUpdated")]}
          rows={customers.map((u) => [u.name || "", u.email, formatDate(u.created_at), formatDate(u.updated_at)])}
        />
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-64 animate-pulse bg-muted rounded-lg" />
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableSortHead sort={srt} column="name" firstOrder="asc">
                      {t("customers.customer")}
                    </DataTableSortHead>
                    <DataTableSortHead sort={srt} column="email" firstOrder="asc">
                      {t("common.email")}
                    </DataTableSortHead>
                    <DataTableSortHead sort={srt} column="created_at" firstOrder="desc">
                      {t("customers.joined")}
                    </DataTableSortHead>
                    <DataTableHead>{t("customers.lastUpdated")}</DataTableHead>
                    <DataTableHead className="w-28">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {customers.length === 0 && <DataTableEmpty colSpan={5} message={t("customers.empty")} />}
                  {customers.map((u) => (
                    <DataTableRow key={u.id}>
                      <DataTableCell>
                        <div className="flex items-center gap-3">
                          {u.avatar_url ? (
                            <img src={u.avatar_url} className="h-8 w-8 rounded-full" alt="" />
                          ) : (
                            <div className="h-8 w-8 rounded-full bg-muted flex items-center justify-center text-xs font-bold">
                              {u.name?.charAt(0)?.toUpperCase() || u.email.charAt(0).toUpperCase()}
                            </div>
                          )}
                          <span className="font-medium">{u.name || "-"}</span>
                        </div>
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground">{u.email}</DataTableCell>
                      <DataTableCell className="text-muted-foreground text-xs">
                        {formatDate(u.created_at)}
                      </DataTableCell>
                      <DataTableCell className="text-muted-foreground text-xs">
                        {formatDate(u.updated_at)}
                      </DataTableCell>
                      <DataTableCell>
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-8 w-8"
                            title={t("customers.detail")}
                            aria-label={t("customers.detail")}
                            onClick={() => setViewingUser(u.id)}
                          >
                            <Eye className="h-4 w-4" />
                          </Button>
                          {/* Export downloads the same document the
                              user can pull from their own privacy
                              page — the action is audited server-side
                              either way. */}
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-8 w-8"
                            title={t("customers.exportAction")}
                            aria-label={t("customers.exportAction")}
                            disabled={exportMut.isPending}
                            onClick={() => exportMut.mutate(u.id)}
                          >
                            <Download className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            className="h-8 w-8"
                            title={t("customers.anonymizeAction")}
                            aria-label={t("customers.anonymizeAction")}
                            onClick={() => setAnonymizing(u)}
                          >
                            <UserX className="h-4 w-4 text-destructive" />
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
                />
              )}
            </>
          )}
        </CardContent>
      </Card>

      <CustomerDetailDialog
        userId={viewingUser}
        open={!!viewingUser}
        onOpenChange={(open) => {
          if (!open) setViewingUser(null)
        }}
      />

      {/* Anonymize is the danger action: the confirmation spells out
          what is erased and — like the portal privacy page — what is
          preserved, before anything irreversible happens. */}
      <AlertDialog open={!!anonymizing} onOpenChange={(open) => !open && setAnonymizing(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("customers.anonymizeTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {anonymizing ? `${anonymizing.email} — ` : ""}
              {t("customers.anonymizeDesc")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              disabled={anonymizeMut.isPending}
              onClick={() => anonymizing && anonymizeMut.mutate(anonymizing.id)}
            >
              {t("customers.anonymizeConfirm")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>

      {/* The receipt: the new anonymized identity and the records
          that were kept, worth reading before the row moves on. */}
      <Dialog open={!!anonymizeResult} onOpenChange={(open) => !open && setAnonymizeResult(null)}>
        <DialogContent>
          <DialogHeader>
            <DialogTitle>{t("customers.anonymizeResultTitle")}</DialogTitle>
            <DialogDescription>{anonymizeResult?.note}</DialogDescription>
          </DialogHeader>
          {anonymizeResult && (
            <DialogBody className="space-y-3 text-sm">
              <div>
                <p className="text-xs text-muted-foreground">{t("customers.anonymizeResultEmail")}</p>
                <code className="text-xs bg-muted px-1.5 py-0.5 rounded">{anonymizeResult.anonymized_email}</code>
              </div>
              <div>
                <p className="text-xs text-muted-foreground">{t("customers.anonymizeResultRetained")}</p>
                <p>{anonymizeResult.retained.join(", ")}</p>
              </div>
            </DialogBody>
          )}
          <DialogFooter>
            <Button variant="outline" onClick={() => setAnonymizeResult(null)}>
              {t("common.close")}
            </Button>
          </DialogFooter>
        </DialogContent>
      </Dialog>
    </div>
  )
}

function CustomerDetailDialog({
  userId,
  open,
  onOpenChange,
}: {
  userId: string | null
  open: boolean
  onOpenChange: (open: boolean) => void
}) {
  const { t } = useI18n()
  const { data, isLoading } = useQuery({
    queryKey: ["admin", "user-detail", userId],
    queryFn: () => admin.getUserDetail(userId!),
    enabled: !!userId,
  })

  const detail: UserDetail | undefined = data

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="max-w-3xl h-[min(760px,85vh)]">
        <DialogHeader>
          <DialogTitle>{t("customers.detail")}</DialogTitle>
          <DialogDescription>{detail?.user?.email || t("common.loading")}</DialogDescription>
        </DialogHeader>
        {/* Fixed height; the tab bar stays put and each tab scrolls. */}
        <DialogBody className="flex flex-col overflow-hidden">
          {isLoading || !detail ? (
            <div className="space-y-4">
              <div className="h-24 bg-muted rounded-lg animate-pulse" />
              <div className="h-48 bg-muted rounded-lg animate-pulse" />
            </div>
          ) : (
            <Tabs defaultValue="overview" className="flex min-h-0 flex-1 flex-col">
              <TabsList className="shrink-0">
                <TabsTrigger value="overview">{t("customers.overview")}</TabsTrigger>
                <TabsTrigger value="licenses">{t("customers.licenses")}</TabsTrigger>
                <TabsTrigger value="subscriptions">{t("customers.subscriptions")}</TabsTrigger>
                <TabsTrigger value="activity">{t("customers.activity")}</TabsTrigger>
              </TabsList>

              {/* Overview Tab */}
              <TabsContent value="overview" className="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]">
                <div className="space-y-4">
                  {/* User info */}
                  <Card>
                    <CardContent className="pt-6">
                      <div className="flex items-center gap-4">
                        {detail.user.avatar_url ? (
                          <img src={detail.user.avatar_url} className="h-14 w-14 rounded-full" alt="" />
                        ) : (
                          <div className="h-14 w-14 rounded-full bg-muted flex items-center justify-center text-lg font-bold">
                            {detail.user.name?.charAt(0)?.toUpperCase() || detail.user.email.charAt(0).toUpperCase()}
                          </div>
                        )}
                        <div>
                          <h3 className="text-lg font-semibold">{detail.user.name || "-"}</h3>
                          <p className="text-sm text-muted-foreground">{detail.user.email}</p>
                          <div className="flex gap-4 mt-1 text-xs text-muted-foreground">
                            <span>
                              {t("customers.joined")} {formatDate(detail.user.created_at)}
                            </span>
                            <span>
                              {t("customers.lastUpdated")} {formatDate(detail.user.updated_at)}
                            </span>
                          </div>
                        </div>
                      </div>
                    </CardContent>
                  </Card>

                  {/* Summary stats */}
                  <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-5">
                    {[
                      { label: t("analytics.totalLicenses"), value: detail.licenses?.length ?? 0 },
                      {
                        label: t("analytics.active"),
                        value: detail.licenses?.filter((l) => l.status === "active").length ?? 0,
                      },
                      { label: t("customers.totalUsage"), value: detail.total_usage ?? 0 },
                      { label: t("customers.activeSeats"), value: detail.active_seats ?? 0 },
                      { label: t("analytics.activations"), value: detail.activations ?? 0 },
                    ].map((s) => (
                      <Card key={s.label}>
                        <CardContent className="pt-4 pb-3 text-center">
                          <div className="text-2xl font-bold">{s.value.toLocaleString()}</div>
                          <p className="text-xs text-muted-foreground">{s.label}</p>
                        </CardContent>
                      </Card>
                    ))}
                  </div>
                </div>
              </TabsContent>

              {/* Licenses Tab */}
              <TabsContent value="licenses" className="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]">
                {(detail.licenses || []).length === 0 ? (
                  <Card>
                    <CardContent className="py-8">
                      <p className="text-sm text-muted-foreground text-center">No licenses found.</p>
                    </CardContent>
                  </Card>
                ) : (
                  <DataTable>
                    <DataTableHeader>
                      <DataTableRow>
                        <DataTableHead>{t("common.product")}</DataTableHead>
                        <DataTableHead>{t("common.plan")}</DataTableHead>
                        <DataTableHead>{t("common.status")}</DataTableHead>
                        <DataTableHead>{t("licenses.licenseKey")}</DataTableHead>
                        <DataTableHead>{t("licenses.validUntil")}</DataTableHead>
                        <DataTableHead>{t("common.created")}</DataTableHead>
                      </DataTableRow>
                    </DataTableHeader>
                    <DataTableBody>
                      {detail.licenses.map((l) => (
                        <DataTableRow key={l.id}>
                          <DataTableCell className="font-medium">{l.product?.name || l.product_id}</DataTableCell>
                          <DataTableCell>{l.plan?.name || l.plan_id}</DataTableCell>
                          <DataTableCell>
                            <Badge className={statusColor(l.status)}>
                              {t(`status.${l.status}` as TranslationKeys)}
                            </Badge>
                          </DataTableCell>
                          <DataTableCell>
                            {/* Only the tail — the key itself is revealed
                              one at a time from the licenses page, and
                              each reveal is audited. */}
                            <code className="text-xs bg-muted px-1.5 py-0.5 rounded font-mono">
                              {data?.license_key_hints?.[l.id] ? `KG-••••-${data.license_key_hints[l.id]}` : "••••"}
                            </code>
                          </DataTableCell>
                          <DataTableCell className="text-xs text-muted-foreground">
                            {l.valid_until ? formatDate(l.valid_until) : "-"}
                          </DataTableCell>
                          <DataTableCell className="text-xs text-muted-foreground">
                            {formatDate(l.created_at)}
                          </DataTableCell>
                        </DataTableRow>
                      ))}
                    </DataTableBody>
                  </DataTable>
                )}
              </TabsContent>

              {/* Subscriptions Tab */}
              <TabsContent value="subscriptions" className="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]">
                {(detail.subscriptions || []).length === 0 ? (
                  <Card>
                    <CardContent className="py-8">
                      <p className="text-sm text-muted-foreground text-center">No subscriptions found.</p>
                    </CardContent>
                  </Card>
                ) : (
                  <DataTable>
                    <DataTableHeader>
                      <DataTableRow>
                        <DataTableHead>{t("common.plan")}</DataTableHead>
                        <DataTableHead>{t("common.status")}</DataTableHead>
                        <DataTableHead>{t("customers.provider")}</DataTableHead>
                        <DataTableHead>{t("customers.periodRange")}</DataTableHead>
                        <DataTableHead>{t("customers.cancelAtEnd")}</DataTableHead>
                        <DataTableHead>{t("common.created")}</DataTableHead>
                      </DataTableRow>
                    </DataTableHeader>
                    <DataTableBody>
                      {detail.subscriptions.map((sub) => (
                        <DataTableRow key={sub.id}>
                          <DataTableCell className="font-medium">{sub.plan?.name || sub.plan_id}</DataTableCell>
                          <DataTableCell>
                            <Badge className={statusColor(sub.status)}>
                              {t(`status.${sub.status}` as TranslationKeys)}
                            </Badge>
                          </DataTableCell>
                          <DataTableCell className="text-muted-foreground">{sub.payment_provider || "-"}</DataTableCell>
                          <DataTableCell className="text-xs text-muted-foreground">
                            <div>
                              {sub.current_period_start ? formatDate(sub.current_period_start) : "-"}
                              {" - "}
                              {sub.current_period_end ? formatDate(sub.current_period_end) : "-"}
                            </div>
                            {sub.trial_start && (
                              <div className="text-violet-600 mt-0.5">
                                Trial: {formatDate(sub.trial_start)} - {formatDate(sub.trial_end)}
                              </div>
                            )}
                          </DataTableCell>
                          <DataTableCell>
                            {sub.cancel_at_period_end ? (
                              <Badge variant="destructive">{t("common.yes")}</Badge>
                            ) : (
                              <span className="text-muted-foreground text-xs">{t("common.no")}</span>
                            )}
                          </DataTableCell>
                          <DataTableCell className="text-xs text-muted-foreground">
                            {formatDate(sub.created_at)}
                          </DataTableCell>
                        </DataTableRow>
                      ))}
                    </DataTableBody>
                  </DataTable>
                )}
              </TabsContent>

              {/* Activity Tab */}
              <TabsContent value="activity" className="min-h-0 flex-1 overflow-y-auto [scrollbar-gutter:stable]">
                {(detail.recent_audit_logs || []).length === 0 ? (
                  <Card>
                    <CardContent className="py-8">
                      <p className="text-sm text-muted-foreground text-center">No recent activity.</p>
                    </CardContent>
                  </Card>
                ) : (
                  <div className="space-y-2">
                    {detail.recent_audit_logs.map((a) => (
                      <div key={a.id} className="flex items-center gap-3 py-2 border-b last:border-0 text-sm">
                        <span className="text-xs text-muted-foreground w-36 shrink-0">{formatDate(a.created_at)}</span>
                        <Badge variant="outline" className="shrink-0">
                          {a.entity}
                        </Badge>
                        <Badge
                          className={
                            a.action.includes("create")
                              ? "bg-emerald-100 text-emerald-800"
                              : a.action.includes("delete") || a.action.includes("revoke")
                                ? "bg-red-100 text-red-800"
                                : a.action.includes("update")
                                  ? "bg-blue-100 text-blue-800"
                                  : a.action.includes("suspend")
                                    ? "bg-orange-100 text-orange-800"
                                    : "bg-gray-100 text-gray-800"
                          }
                        >
                          {a.action}
                        </Badge>
                        <span
                          className="font-mono text-xs text-muted-foreground truncate max-w-[180px]"
                          title={a.entity_id}
                        >
                          {a.entity_id.length > 12 ? `${a.entity_id.slice(0, 12)}...` : a.entity_id}
                        </span>
                      </div>
                    ))}
                  </div>
                )}
              </TabsContent>
            </Tabs>
          )}
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}
