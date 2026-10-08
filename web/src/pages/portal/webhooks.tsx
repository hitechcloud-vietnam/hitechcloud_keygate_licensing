import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Copy, Link2, Pencil, Plus, Send, Trash2 } from "lucide-react"
import { useState } from "react"
import { ListEmptyState } from "@/components/empty-state"
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
import { useI18n } from "@/i18n"
import type { PortalWebhook } from "@/lib/api"
import { portal } from "@/lib/api"
import { formatDate } from "@/lib/utils"
import { WEBHOOK_EVENTS } from "@/lib/webhook-events"

// The customer's own webhook endpoints. Contract (pinned):
//   GET    /portal/webhooks?limit=&offset=  -> { webhooks, total, limit, offset }
//   POST   /portal/webhooks  { url, events, active? } -> { webhook, secret }
//   PATCH  /portal/webhooks/:id { url?, events?, active? }
//   DELETE /portal/webhooks/:id
//   POST   /portal/webhooks/:id/test
// The plaintext secret is returned exactly once, on create.

export default function PortalWebhooksPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [creating, setCreating] = useState(false)
  const [editing, setEditing] = useState<PortalWebhook | null>(null)
  const [deleting, setDeleting] = useState<PortalWebhook | null>(null)
  const [freshSecret, setFreshSecret] = useState<string | null>(null)

  const pg = useServerPagination(10)
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "webhooks", pg.page, pg.pageSize],
    queryFn: () => portal.listPortalWebhooks(pg.params),
  })

  const { items: webhooks, total, totalPages } = pg.from(data, data?.webhooks)

  const deleteMut = useMutation({
    mutationFn: (id: string) => portal.deletePortalWebhook(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "webhooks"] })
      showToast(t("portal.whDeleted"), "success")
      setDeleting(null)
    },
    onError: (e: Error) => toastError(e),
  })

  const toggleMut = useMutation({
    mutationFn: (wh: PortalWebhook) => portal.updatePortalWebhook(wh.id, { active: !wh.active }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "webhooks"] })
      showToast(t("portal.whUpdated"), "success")
    },
    onError: (e: Error) => toastError(e),
  })

  const testMut = useMutation({
    mutationFn: (id: string) => portal.testPortalWebhook(id),
    onSuccess: () => showToast(t("portal.whTestSent"), "success"),
    onError: (e: Error) => toastError(e),
  })

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight">{t("portal.whTitle")}</h1>
          <p className="text-muted-foreground">{t("portal.whDesc")}</p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="h-4 w-4 mr-2" /> {t("portal.whNew")}
        </Button>
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-32 animate-pulse bg-muted rounded-lg" />
          ) : webhooks.length === 0 ? (
            // No filters on this page — an empty table is a first-run
            // story: say what webhooks are for, offer the create.
            <ListEmptyState
              icon={Link2}
              title={t("portal.whEmpty")}
              description={t("empty.webhooks.desc")}
              action={{ label: t("portal.whNew"), onClick: () => setCreating(true) }}
            />
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    <DataTableHead>{t("portal.whUrl")}</DataTableHead>
                    <DataTableHead>{t("portal.whEvents")}</DataTableHead>
                    <DataTableHead>{t("common.status")}</DataTableHead>
                    <DataTableHead>{t("portal.whSecretPrefix")}</DataTableHead>
                    <DataTableHead>{t("portal.whLastDelivery")}</DataTableHead>
                    <DataTableHead className="w-32 text-right">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {webhooks.map((wh: PortalWebhook) => (
                    <DataTableRow key={wh.id}>
                      <DataTableCell className="font-mono text-xs max-w-[220px] truncate">{wh.url}</DataTableCell>
                      <DataTableCell>
                        <div className="flex flex-wrap gap-1">
                          {wh.events.slice(0, 2).map((e) => (
                            <Badge key={e} variant="secondary" className="text-xs">
                              {e}
                            </Badge>
                          ))}
                          {wh.events.length > 2 && (
                            <Badge variant="outline" className="text-xs">
                              +{wh.events.length - 2}
                            </Badge>
                          )}
                        </div>
                      </DataTableCell>
                      <DataTableCell>
                        <Badge className={wh.active ? "bg-emerald-100 text-emerald-800" : "bg-gray-100 text-gray-800"}>
                          {wh.active ? t("portal.whActive") : t("webhooks.inactive")}
                        </Badge>
                      </DataTableCell>
                      <DataTableCell className="font-mono text-xs">{wh.secret_prefix}</DataTableCell>
                      <DataTableCell className="text-muted-foreground">
                        {wh.last_delivery_at ? formatDate(wh.last_delivery_at) : t("portal.apiKeysNever")}
                      </DataTableCell>
                      <DataTableCell>
                        <div className="flex justify-end gap-1">
                          <Button
                            variant="ghost"
                            size="icon"
                            disabled={testMut.isPending && testMut.variables === wh.id}
                            onClick={() => testMut.mutate(wh.id)}
                            aria-label={t("portal.whTest")}
                          >
                            <Send className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            disabled={toggleMut.isPending && toggleMut.variables?.id === wh.id}
                            onClick={() => toggleMut.mutate(wh)}
                            aria-label={wh.active ? t("webhooks.disable") : t("webhooks.enable")}
                          >
                            <Check className={wh.active ? "h-4 w-4 text-emerald-600" : "h-4 w-4"} />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            onClick={() => setEditing(wh)}
                            aria-label={t("common.edit")}
                          >
                            <Pencil className="h-4 w-4" />
                          </Button>
                          <Button
                            variant="ghost"
                            size="icon"
                            onClick={() => setDeleting(wh)}
                            aria-label={t("common.delete")}
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

      {(creating || editing) && (
        <WebhookFormDialog
          webhook={editing}
          onClose={() => {
            setCreating(false)
            setEditing(null)
          }}
          onSaved={(secret) => {
            setCreating(false)
            setEditing(null)
            qc.invalidateQueries({ queryKey: ["portal", "webhooks"] })
            if (secret) {
              setFreshSecret(secret)
              showToast(t("portal.whCreated"), "success")
            } else {
              showToast(t("portal.whUpdated"), "success")
            }
          }}
        />
      )}

      <SecretDialog secret={freshSecret} onClose={() => setFreshSecret(null)} />

      <AlertDialog open={!!deleting} onOpenChange={(open) => !open && setDeleting(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("portal.whDeleteTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("portal.whDeleteDesc")}</AlertDialogDescription>
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

// Create + edit share one form. On create the backend returns the
// plaintext secret once; on edit it never does, so onSaved gets null.
function WebhookFormDialog({
  webhook,
  onClose,
  onSaved,
}: {
  webhook: PortalWebhook | null
  onClose: () => void
  onSaved: (secret: string | null) => void
}) {
  const { t } = useI18n()
  const [url, setUrl] = useState(webhook?.url || "")
  const [selectedEvents, setSelectedEvents] = useState<string[]>(webhook?.events || [])
  const [active, setActive] = useState(webhook?.active ?? true)
  const isEdit = !!webhook

  const toggleEvent = (event: string) => {
    setSelectedEvents((prev) => (prev.includes(event) ? prev.filter((e) => e !== event) : [...prev, event]))
  }

  const saveMut = useMutation({
    mutationFn: async (): Promise<string | null> => {
      const trimmedUrl = url.trim()
      if (isEdit && webhook) {
        await portal.updatePortalWebhook(webhook.id, { url: trimmedUrl, events: selectedEvents, active })
        return null
      }
      const res = await portal.createPortalWebhook({ url: trimmedUrl, events: selectedEvents, active })
      return res.secret
    },
    onSuccess: (secret) => onSaved(secret),
    onError: (e: Error) => toastError(e),
  })

  const validUrl = /^https?:\/\/.+/i.test(url.trim())
  const canSubmit = validUrl && selectedEvents.length > 0 && !saveMut.isPending

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle>{isEdit ? t("common.edit") : t("portal.whNew")}</DialogTitle>
          <DialogDescription>{t("portal.whCreateDesc")}</DialogDescription>
        </DialogHeader>
        <form
          onSubmit={(e) => {
            e.preventDefault()
            if (canSubmit) saveMut.mutate()
          }}
          className="flex min-h-0 flex-1 flex-col gap-4"
        >
          <DialogBody className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="wh-url">{t("portal.whUrl")}</Label>
              <Input
                id="wh-url"
                type="url"
                value={url}
                onChange={(e) => setUrl(e.target.value)}
                placeholder={t("portal.whUrlPlaceholder")}
                required
              />
            </div>
            <div className="space-y-2">
              <Label>{t("portal.whEvents")}</Label>
              <div className="grid grid-cols-1 gap-2 sm:grid-cols-2 max-h-64 overflow-y-auto">
                {WEBHOOK_EVENTS.map((event) => (
                  <label
                    key={event}
                    className="flex items-center gap-2 rounded border px-3 py-2 text-sm cursor-pointer hover:bg-accent"
                  >
                    <input
                      type="checkbox"
                      checked={selectedEvents.includes(event)}
                      onChange={() => toggleEvent(event)}
                      className="rounded"
                    />
                    {event}
                  </label>
                ))}
              </div>
              {selectedEvents.length === 0 && (
                <p className="text-xs text-muted-foreground">{t("portal.whEventsRequired")}</p>
              )}
            </div>
            <label className="flex items-center gap-2 text-sm">
              <input
                type="checkbox"
                checked={active}
                onChange={(e) => setActive(e.target.checked)}
                className="rounded"
              />
              {t("portal.whActive")}
            </label>
          </DialogBody>
          <DialogFooter>
            <Button type="button" variant="outline" onClick={onClose}>
              {t("common.cancel")}
            </Button>
            <Button type="submit" disabled={!canSubmit}>
              {saveMut.isPending ? t("common.loading") : isEdit ? t("common.save") : t("common.create")}
            </Button>
          </DialogFooter>
        </form>
      </DialogContent>
    </Dialog>
  )
}

// The secret is shown exactly once, on create. There is no way back to it.
function SecretDialog({ secret, onClose }: { secret: string | null; onClose: () => void }) {
  const { t } = useI18n()
  const [copied, setCopied] = useState(false)

  const copy = () => {
    navigator.clipboard.writeText(secret || "")
    setCopied(true)
    setTimeout(() => setCopied(false), 2000)
  }

  return (
    <Dialog open={!!secret} onOpenChange={(open) => !open && onClose()}>
      <DialogContent className="max-w-lg">
        <DialogHeader>
          <DialogTitle className="flex items-center gap-2">
            <Link2 className="h-5 w-5 text-amber-500" /> {t("portal.whSecretTitle")}
          </DialogTitle>
          <DialogDescription>{t("portal.whSecretDesc")}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-3">
          <div className="flex items-center gap-2">
            <Input readOnly value={secret || ""} className="font-mono text-xs" aria-label={t("portal.whSecretTitle")} />
            <Button onClick={copy} variant="outline" className="shrink-0">
              {copied ? <Check className="h-4 w-4 mr-1" /> : <Copy className="h-4 w-4 mr-1" />}
              {copied ? t("portal.whSecretCopied") : t("portal.whSecretCopy")}
            </Button>
          </div>
          <p className="text-sm text-amber-600">{t("portal.whSecretWarn")}</p>
        </DialogBody>
        <DialogFooter>
          <Button onClick={onClose}>{t("common.close")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
