import { useMutation } from "@tanstack/react-query"
import { Download, ShieldAlert, Trash2 } from "lucide-react"
import { useState } from "react"
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
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"
import { useAuth } from "@/hooks/use-auth"
import { useI18n } from "@/i18n"
import { auth, portal } from "@/lib/api"

// Privacy & data (plan §82): the two self-service rights in one place.
//   GET  /portal/export           -> the whole account as one JSON document
//   POST /portal/delete-account   -> anonymize the account (confirmed)
//
// The deletion is ANONYMIZATION, not a hard delete: the identity is
// erased and every credential revoked, while orders, invoices,
// refunds, licenses and audit logs are preserved for legal and
// accounting reasons — the page says so BEFORE the button, because a
// "delete" that quietly keeps half the rows must not be a surprise.

// downloadJSON hands the export to the browser as a file. Same shape
// as the CSV helper in components/export-csv: a transient object URL,
// a synthetic anchor click, then the URL is released.
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

export default function AccountPrivacyPage() {
  const { t } = useI18n()
  const { user } = useAuth()
  const [confirmText, setConfirmText] = useState("")
  const [confirmOpen, setConfirmOpen] = useState(false)

  const exportMut = useMutation({
    mutationFn: () => portal.exportAccount(),
    onSuccess: (data) => {
      downloadJSON(`account-export-${new Date().toISOString().slice(0, 10)}`, data)
      showToast(t("privacy.exportDone"), "success")
    },
    onError: (e: Error) => toastError(e),
  })

  const deleteMut = useMutation({
    mutationFn: () => portal.deleteAccount(confirmText.trim()),
    onSuccess: async () => {
      setConfirmOpen(false)
      showToast(t("privacy.deleted"), "success")
      // The account no longer exists: clear the session cookie and
      // land on the public page. logout is best-effort — the server
      // already revoked every token of this account.
      try {
        await auth.logout()
      } catch {
        // the session may already be gone; the redirect below stands
      }
      window.location.href = "/"
    },
    onError: (e: Error) => toastError(e),
  })

  // The typed confirmation is the account's own email. Compared
  // folded, exactly like the server compares addresses — the button
  // and the endpoint must agree on what "confirmed" means.
  const email = user?.email ?? ""
  const confirmed = email !== "" && confirmText.trim().toLowerCase() === email.toLowerCase()

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("privacy.title")}</h1>
        <p className="text-muted-foreground">{t("privacy.desc")}</p>
      </div>

      {/* Export */}
      <Card>
        <CardHeader>
          <CardTitle className="text-lg flex items-center gap-2">
            <Download className="h-4 w-4" /> {t("privacy.exportTitle")}
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-sm text-muted-foreground">{t("privacy.exportDesc")}</p>
          <Button onClick={() => exportMut.mutate()} disabled={exportMut.isPending}>
            <Download className="h-4 w-4 mr-2" />
            {exportMut.isPending ? t("common.loading") : t("privacy.exportButton")}
          </Button>
        </CardContent>
      </Card>

      {/* Danger zone */}
      <Card className="border-destructive/50">
        <CardHeader>
          <CardTitle className="text-lg flex items-center gap-2 text-destructive">
            <ShieldAlert className="h-4 w-4" /> {t("privacy.deleteTitle")}
          </CardTitle>
        </CardHeader>
        <CardContent className="space-y-4">
          <p className="text-sm text-muted-foreground">{t("privacy.deleteDesc")}</p>

          <div className="rounded-lg border border-destructive/30 bg-destructive/5 p-4 space-y-2 text-sm">
            <p className="flex items-start gap-2">
              <Trash2 className="h-4 w-4 mt-0.5 shrink-0 text-destructive" />
              <span>{t("privacy.deleteErased")}</span>
            </p>
            <Separator />
            <p className="text-muted-foreground">{t("privacy.deleteRetained")}</p>
          </div>

          <div className="space-y-2">
            <Label htmlFor="privacy-confirm">{t("privacy.deleteConfirmLabel")}</Label>
            <Input
              id="privacy-confirm"
              value={confirmText}
              onChange={(e) => setConfirmText(e.target.value)}
              placeholder={email}
              autoComplete="off"
            />
            <p className="text-xs text-muted-foreground">{t("privacy.deleteConfirmHint")}</p>
          </div>

          <Button
            variant="destructive"
            disabled={!confirmed || deleteMut.isPending}
            onClick={() => setConfirmOpen(true)}
          >
            <Trash2 className="h-4 w-4 mr-2" /> {t("privacy.deleteButton")}
          </Button>
        </CardContent>
      </Card>

      <AlertDialog open={confirmOpen} onOpenChange={(open) => !open && setConfirmOpen(false)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("privacy.deleteDialogTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("privacy.deleteDialogBody")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => deleteMut.mutate()}
            >
              {t("privacy.deleteDialogConfirm")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}
