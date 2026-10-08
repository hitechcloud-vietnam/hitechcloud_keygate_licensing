import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { MonitorSmartphone, Plus, Trash2, Users } from "lucide-react"
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
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Dialog, DialogBody, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Separator } from "@/components/ui/separator"
import { useAuth } from "@/hooks/use-auth"
import { useI18n } from "@/i18n"
import type { Activation, PortalLicense, Seat } from "@/lib/api"
import { portal } from "@/lib/api"
import { formatDate } from "@/lib/utils"

// Devices & activations for every licence, plus team-seat management.
// The dedicated activation endpoint requires the licence owner OR an
// *accepted* seat (pending invites confer no authority — see
// internal/handler/portal_activations.go), and seat mutation is gated to
// the owner or an accepted admin. We mirror those gates so the UI never
// offers an action the server would refuse.

function useLicenseGuards(lic: PortalLicense) {
  const { user } = useAuth()
  const myEmail = user?.email?.toLowerCase() ?? ""
  const isOwner = !!myEmail && lic.email?.toLowerCase() === myEmail
  const mySeat = (lic.seats || []).find((s) => s.email.toLowerCase() === myEmail && !s.removed_at)
  const isAcceptedSeat = !!mySeat?.accepted_at
  return {
    isOwner,
    isAcceptedSeat,
    isAcceptedAdmin: isAcceptedSeat && mySeat?.role === "admin",
    canManageActivations: isOwner || isAcceptedSeat,
  }
}

export default function PortalDevicesPage() {
  const { t } = useI18n()
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "licenses"],
    queryFn: portal.licenses,
  })

  const licenses = data?.licenses || []

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("portal.devicesTitle")}</h1>
        <p className="text-muted-foreground">{t("portal.devicesDesc")}</p>
      </div>

      {isLoading ? (
        <div className="space-y-4">
          {[1, 2].map((i) => (
            <div key={i} className="h-40 animate-pulse bg-muted rounded-lg" />
          ))}
        </div>
      ) : licenses.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center">
            <MonitorSmartphone className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
            <p className="text-lg font-medium">{t("portal.devicesEmpty")}</p>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-4">
          {licenses.map((lic) => (
            <DeviceLicenseCard key={lic.id} license={lic} />
          ))}
        </div>
      )}
    </div>
  )
}

function DeviceLicenseCard({ license: lic }: { license: PortalLicense }) {
  const { t } = useI18n()
  const guards = useLicenseGuards(lic)
  const productType = lic.product?.type || "perpetual"
  const seatCap = lic.plan?.max_seats ?? 0
  const showSeats = (productType === "saas" || productType === "hybrid") && seatCap !== 1

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div>
            <CardTitle className="text-lg">{lic.product?.name || t("common.product")}</CardTitle>
            <p className="text-sm text-muted-foreground mt-1">{lic.plan?.name}</p>
          </div>
          <Badge variant="secondary">
            {t("portal.activeDevices")} {lic.activation_count ?? lic.activations?.length ?? 0}
            {lic.plan?.max_activations ? `/${lic.plan.max_activations}` : ""}
          </Badge>
        </div>
      </CardHeader>
      <CardContent className="space-y-6">
        <ActivationsSection license={lic} canManage={guards.canManageActivations} />
        {showSeats && (
          <>
            <Separator />
            <SeatsSection license={lic} canManage={guards.isOwner || guards.isAcceptedAdmin} seatCap={seatCap} />
          </>
        )}
      </CardContent>
    </Card>
  )
}

function ActivationsSection({ license, canManage }: { license: PortalLicense; canManage: boolean }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [removing, setRemoving] = useState<Activation | null>(null)

  const actQuery = useQuery({
    queryKey: ["portal", "activations", license.id],
    queryFn: () => portal.listActivations(license.license_key),
    enabled: canManage,
  })

  const removeMut = useMutation({
    mutationFn: (activationId: string) => portal.removeActivation(license.license_key, activationId),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "activations", license.id] })
      qc.invalidateQueries({ queryKey: ["portal", "licenses"] })
    },
    onSettled: () => setRemoving(null),
    onError: (e: Error) => toastError(e),
  })

  // Read-only fallback for unauthorised viewers (pending seats) and on a
  // transient query error: the embedded list never falsely reads empty.
  const readOnly = !canManage || actQuery.isError
  const activations = readOnly ? license.activations || [] : actQuery.data?.activations || []
  const max = readOnly ? (license.plan?.max_activations ?? 0) : (actQuery.data?.max ?? 0)

  return (
    <div className="space-y-3">
      <p className="text-sm font-medium flex items-center gap-2">
        <MonitorSmartphone className="h-4 w-4 text-muted-foreground" />
        {t("portal.activeDevices")} ({activations.length}
        {max > 0 ? `/${max}` : ""})
      </p>

      {actQuery.isLoading && !readOnly ? (
        <div className="h-16 animate-pulse bg-muted rounded-lg" />
      ) : activations.length === 0 ? (
        <p className="text-sm text-muted-foreground py-4 text-center">{t("portal.noDevices")}</p>
      ) : (
        <div className="space-y-2">
          {activations.map((act) => (
            <div key={act.id} className="flex items-center justify-between bg-muted/50 rounded px-3 py-2 text-sm">
              <div className="min-w-0 flex-1">
                <div className="flex items-center gap-2">
                  <code className="text-xs truncate">{act.identifier}</code>
                  {act.label && <span className="text-muted-foreground text-xs">({act.label})</span>}
                  <span className="text-xs text-muted-foreground capitalize">{act.identifier_type}</span>
                </div>
                <p className="text-xs text-muted-foreground mt-0.5">
                  {t("portal.lastVerified")} {formatDate(act.last_verified)}
                </p>
              </div>
              {canManage && !readOnly && (
                <Button
                  variant="ghost"
                  size="icon"
                  className="h-7 w-7 shrink-0 text-destructive"
                  disabled={removeMut.isPending && removeMut.variables === act.id}
                  onClick={() => setRemoving(act)}
                  aria-label={t("portal.removeDevice")}
                >
                  <Trash2 className="h-3.5 w-3.5" />
                </Button>
              )}
            </div>
          ))}
        </div>
      )}

      <AlertDialog open={!!removing} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("portal.removeDeviceTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("portal.removeDeviceDesc").replace("{device}", removing?.label || removing?.identifier || "")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => removing && removeMut.mutate(removing.id)}
            >
              {t("portal.removeDevice")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function SeatsSection({
  license,
  canManage,
  seatCap,
}: {
  license: PortalLicense
  canManage: boolean
  seatCap: number
}) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [showInvite, setShowInvite] = useState(false)
  const [removing, setRemoving] = useState<Seat | null>(null)

  const seatsQuery = useQuery({
    queryKey: ["portal", "seats", license.id],
    queryFn: () => portal.listSeats(license.license_key),
  })

  const removeMut = useMutation({
    mutationFn: (seatId: string) => portal.removeSeat({ license_key: license.license_key, seat_id: seatId }),
    onSuccess: () => {
      qc.invalidateQueries({ queryKey: ["portal", "seats", license.id] })
    },
    onSettled: () => setRemoving(null),
    onError: (e: Error) => toastError(e),
  })

  const seats = seatsQuery.data?.seats || []
  const activeCount = seats.filter((s) => !s.removed_at).length
  // maxSeats=0 means "no limit"; never gate Add in that case.
  const atLimit = seatCap > 0 && activeCount >= seatCap

  return (
    <div className="space-y-3">
      <div className="flex items-center justify-between">
        <p className="text-sm font-medium flex items-center gap-2">
          <Users className="h-4 w-4 text-muted-foreground" />
          {t("portal.teamMembers")} ({activeCount}/{seatCap === 0 ? t("common.unlimitedSymbol") : seatCap})
        </p>
        {canManage && (
          <Button size="sm" disabled={atLimit} onClick={() => setShowInvite(true)}>
            <Plus className="h-4 w-4 mr-1.5" /> {t("portal.addMember")}
          </Button>
        )}
      </div>

      {canManage && atLimit && <p className="text-xs text-muted-foreground">{t("portal.seatLimit")}</p>}

      {seatsQuery.isLoading ? (
        <div className="h-16 animate-pulse bg-muted rounded-lg" />
      ) : seats.length === 0 ? (
        <p className="text-sm text-muted-foreground py-4 text-center">{t("portal.noMembers")}</p>
      ) : (
        <div className="space-y-2">
          {seats.map((s) => (
            <SeatRow
              key={s.id}
              seat={s}
              canManage={canManage}
              onRemove={() => setRemoving(s)}
              isRemoving={removeMut.isPending && removeMut.variables === s.id}
            />
          ))}
        </div>
      )}

      {showInvite && <InviteSeatDialog license={license} onClose={() => setShowInvite(false)} />}

      <AlertDialog open={!!removing} onOpenChange={(o) => !o && setRemoving(null)}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("portal.removeMemberTitle")}</AlertDialogTitle>
            <AlertDialogDescription>
              {t("portal.removeMemberDesc").replace("{email}", removing?.email || "")}
            </AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => removing && removeMut.mutate(removing.id)}
            >
              {t("portal.removeMember")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

function SeatRow({
  seat,
  canManage,
  onRemove,
  isRemoving,
}: {
  seat: Seat
  canManage: boolean
  onRemove: () => void
  isRemoving: boolean
}) {
  const { t } = useI18n()
  const accepted = !!seat.accepted_at
  return (
    <div className="flex items-center justify-between bg-muted/50 rounded px-3 py-2 text-sm">
      <div className="min-w-0 flex-1">
        <div className="flex items-center gap-2">
          <span className="font-medium truncate">{seat.email}</span>
          <Badge variant="secondary" className="text-xs capitalize">
            {seat.role === "admin" ? t("portal.roleAdmin") : t("portal.roleMember")}
          </Badge>
          {!accepted && (
            <Badge variant="outline" className="text-xs">
              {t("licenses.invited")}
            </Badge>
          )}
        </div>
        <p className="text-xs text-muted-foreground mt-0.5">
          {accepted ? `${t("portal.lastVerified")} ${formatDate(seat.accepted_at!)}` : formatDate(seat.invited_at)}
        </p>
      </div>
      {canManage && (
        <Button
          variant="ghost"
          size="icon"
          className="h-7 w-7 shrink-0 text-destructive"
          disabled={isRemoving}
          onClick={onRemove}
          aria-label={t("portal.removeMember")}
        >
          <Trash2 className="h-3.5 w-3.5" />
        </Button>
      )}
    </div>
  )
}

function InviteSeatDialog({ license, onClose }: { license: PortalLicense; onClose: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const [email, setEmail] = useState("")
  const [role, setRole] = useState<"member" | "admin">("member")

  const inviteMut = useMutation({
    mutationFn: () => portal.addSeat({ license_key: license.license_key, email: email.trim(), role }),
    onSuccess: () => {
      showToast(t("licenses.seatInviteSent"), "success")
      qc.invalidateQueries({ queryKey: ["portal", "seats", license.id] })
      onClose()
    },
    onError: (e: Error) => toastError(e),
  })

  const trimmed = email.trim()
  const isEmail = /^[^\s@]+@[^\s@]+\.[^\s@]+$/.test(trimmed)
  const canSubmit = isEmail && !inviteMut.isPending

  return (
    <Dialog open onOpenChange={(o) => !o && onClose()}>
      <DialogContent>
        <DialogHeader>
          <DialogTitle>{t("licenses.inviteTitle")}</DialogTitle>
          <DialogDescription>{t("licenses.inviteDesc")}</DialogDescription>
        </DialogHeader>
        <DialogBody>
          <div className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="device-invite-email">{t("common.email")}</Label>
              <Input
                id="device-invite-email"
                type="email"
                autoComplete="off"
                autoFocus
                value={email}
                onChange={(e) => setEmail(e.target.value)}
                placeholder="teammate@example.com"
                onKeyDown={(e) => {
                  if (e.key === "Enter" && canSubmit) inviteMut.mutate()
                }}
              />
            </div>
            <div className="space-y-2">
              <Label htmlFor="device-invite-role">{t("team.role")}</Label>
              <Select value={role} onValueChange={(v) => setRole(v as "member" | "admin")}>
                <SelectTrigger id="device-invite-role">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  <SelectItem value="member">{t("portal.roleMember")}</SelectItem>
                  <SelectItem value="admin">{t("portal.roleAdmin")}</SelectItem>
                </SelectContent>
              </Select>
            </div>
            <div className="flex justify-end gap-2">
              <Button variant="outline" onClick={onClose}>
                {t("common.cancel")}
              </Button>
              <Button disabled={!canSubmit} onClick={() => inviteMut.mutate()}>
                {t("licenses.sendInvite")}
              </Button>
            </div>
          </div>
        </DialogBody>
      </DialogContent>
    </Dialog>
  )
}
