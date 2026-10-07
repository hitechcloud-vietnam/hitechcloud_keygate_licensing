import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Check, Copy, Key, Plus, Trash2 } from "lucide-react"
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
import type { CustomerAPIKey } from "@/lib/api"
import { portal } from "@/lib/api"
import { formatDate } from "@/lib/utils"

export default function PortalAPIKeysPage() {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [creating, setCreating] = useState(false)
  const [revoking, setRevoking] = useState<CustomerAPIKey | null>(null)
  // The plaintext secret is returned exactly once, on creation. It is
  // held here only long enough to show the one-time dialog — never
  // refetched, never stored, never sent back to the server.
  const [freshSecret, setFreshSecret] = useState<string | null>(null)

  const pg = useServerPagination(10)
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "api-keys", pg.page, pg.pageSize],
    queryFn: () => portal.listPortalAPIKeys(pg.params),
  })

  const { items: keys, total, totalPages } = pg.from(data, data?.api_keys)

  const revokeMut = useMutation({
    mutationFn: (id: string) => portal.revokePortalAPIKey(id),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "api-keys"] })
      showToast(t("portal.apiKeysRevokedToast"), "success")
      setRevoking(null)
    },
    onError: (e: Error) => showToast(e.message, "error"),
  })

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight">{t("portal.apiKeysTitle")}</h1>
          <p className="text-muted-foreground">{t("portal.apiKeysDesc")}</p>
        </div>
        <Button onClick={() => setCreating(true)}>
          <Plus className="h-4 w-4 mr-2" /> {t("portal.apiKeysNew")}
        </Button>
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
                    <DataTableHead>{t("portal.apiKeysPrefix")}</DataTableHead>
                    <DataTableHead>{t("portal.apiKeysScopes")}</DataTableHead>
                    <DataTableHead>{t("common.status")}</DataTableHead>
                    <DataTableHead>{t("portal.apiKeysLastUsed")}</DataTableHead>
                    <DataTableHead>{t("portal.apiKeysExpiresAt")}</DataTableHead>
                    <DataTableHead className="w-24 text-right">{t("common.actions")}</DataTableHead>
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {keys.length === 0 && <DataTableEmpty colSpan={7} message={t("portal.apiKeysEmpty")} />}
                  {keys.map((k: CustomerAPIKey) => {
                    const revoked = !!k.revoked_at
                    return (
                      <DataTableRow key={k.id}>
                        <DataTableCell className="font-medium">{k.name}</DataTableCell>
                        <DataTableCell className="font-mono text-xs">{k.key_prefix}</DataTableCell>
                        <DataTableCell className="text-muted-foreground">{k.scopes || "-"}</DataTableCell>
                        <DataTableCell>
                          <Badge className={revoked ? "bg-red-100 text-red-700" : "bg-emerald-100 text-emerald-800"}>
                            {revoked ? t("portal.apiKeysRevoked") : t("portal.apiKeysActive")}
                          </Badge>
                        </DataTableCell>
                        <DataTableCell className="text-muted-foreground">
                          {k.last_used_at ? formatDate(k.last_used_at) : t("portal.apiKeysNever")}
                        </DataTableCell>
                        <DataTableCell className="text-muted-foreground">
                          {k.expires_at ? formatDate(k.expires_at) : t("portal.apiKeysNever")}
                        </DataTableCell>
                        <DataTableCell>
                          <div className="flex justify-end">
                            <Button
                              variant="ghost"
                              size="icon"
                              disabled={revoked}
                              onClick={() => setRevoking(k)}
                              aria-label={t("portal.apiKeysRevoke")}
                            >
                              <Trash2 className="h-4 w-4 text-destructive" />
                            </Button>
                          </div>
                        </DataTableCell>
                      </DataTableRow>
                    )
                  })}
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

      {creating && (
        <CreateKeyDialog
          onClose={() => setCreating(false)}
          onCreated={(secret) => {
            setCreating(false)
            setFreshSecret(secret)
            qc.invalidateQueries({ queryKey: ["portal", "api-keys"] })
            showToast(t("portal.apiKeysCreatedToast"), "success")
          }}
        />
      )}

      {/* The secret is shown exactly once. There is no way back to it. */}
      <SecretDialog secret={freshSecret} onClose={() => setFreshSecret(null)} />

      <AlertDialog open={!!revoking} onOpenChange={(open) => !open && setRevoking(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("portal.apiKeysRevokeTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("portal.apiKeysRevokeDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => revoking && revokeMut.mutate(revoking.id)}
            >
              {t("portal.apiKeysRevoke")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function CreateKeyDialog({ onClose, onCreated }: { onClose: () => void; onCreated: (secret: string) => void }) {
  const { t } = useI18n()
  const [name, setName] = useState("")
  const [scopes, setScopes] = useState("")
  const [expiresAt, setExpiresAt] = useState("")

  const createMut = useMutation({
    mutationFn: () =>
      portal.createPortalAPIKey({
        name: name.trim(),
        scopes: scopes.trim() || undefined,
        expires_at: expiresAt ? new Date(expiresAt).toISOString() : undefined,
      }),
    onSuccess: (res) => onCreated(res.secret),
    onError: (e: Error) => showToast(e.message, "error"),
  })

  const submit = () => {
    if (!name.trim()) return showToast(t("portal.apiKeysNameRequired"), "error")
    createMut.mutate()
  }

  return (
    <Dialog open onOpenChange={onClose}>
      <DialogContent className="max-w-md">
        <DialogHeader>
          <DialogTitle>{t("portal.apiKeysNew")}</DialogTitle>
          <DialogDescription>{t("portal.apiKeysCreateDesc")}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-4">
          <div className="space-y-2">
            <Label htmlFor="key-name">{t("common.name")}</Label>
            <Input
              id="key-name"
              value={name}
              onChange={(e) => setName(e.target.value)}
              placeholder={t("portal.apiKeysNamePlaceholder")}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="key-scopes">
              {t("portal.apiKeysScopes")} <span className="text-muted-foreground">({t("common.optional")})</span>
            </Label>
            <Input
              id="key-scopes"
              value={scopes}
              onChange={(e) => setScopes(e.target.value)}
              placeholder={t("portal.apiKeysScopesPlaceholder")}
            />
          </div>
          <div className="space-y-2">
            <Label htmlFor="key-expires">
              {t("portal.apiKeysExpiresAt")} <span className="text-muted-foreground">({t("common.optional")})</span>
            </Label>
            <Input
              id="key-expires"
              type="datetime-local"
              value={expiresAt}
              onChange={(e) => setExpiresAt(e.target.value)}
            />
          </div>
        </DialogBody>
        <DialogFooter>
          <Button variant="outline" onClick={onClose}>
            {t("common.cancel")}
          </Button>
          <Button onClick={submit} disabled={createMut.isPending}>
            {t("portal.apiKeysCreate")}
          </Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}

// Shown only in the instant after a key is created. Once dismissed the
// secret is gone from this UI and there is no endpoint that returns it.
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
            <Key className="h-5 w-5 text-amber-500" /> {t("portal.apiKeysSecretTitle")}
          </DialogTitle>
          <DialogDescription>{t("portal.apiKeysSecretDesc")}</DialogDescription>
        </DialogHeader>
        <DialogBody className="space-y-3">
          <div className="flex items-center gap-2">
            <Input
              readOnly
              value={secret || ""}
              className="font-mono text-xs"
              aria-label={t("portal.apiKeysSecretTitle")}
            />
            <Button onClick={copy} variant="outline" className="shrink-0">
              {copied ? <Check className="h-4 w-4 mr-1" /> : <Copy className="h-4 w-4 mr-1" />}
              {copied ? t("portal.apiKeysSecretCopied") : t("portal.apiKeysSecretCopy")}
            </Button>
          </div>
          <p className="text-sm text-amber-600">{t("portal.apiKeysSecretWarn")}</p>
        </DialogBody>
        <DialogFooter>
          <Button onClick={onClose}>{t("common.close")}</Button>
        </DialogFooter>
      </DialogContent>
    </Dialog>
  )
}
