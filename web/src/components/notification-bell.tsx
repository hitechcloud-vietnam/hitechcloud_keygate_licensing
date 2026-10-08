// Notification bell + rows (plan §88). The bell sits in the portal
// header and the admin shell; the panel shows the ten most recent
// rows and links to the full page. Text is rendered CLIENT-SIDE from
// each row's title_key (the event name) through the
// notifications.events.* i18n keys — the server sends no prose.
// Data chips (order number, amounts in integer minor units) come from
// the row's `data`; priority is styling only; timestamps are relative
// and refresh when the window regains focus (simple polling, no
// websockets).
import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { Bell, BellOff, CheckCheck } from "lucide-react"
import { useEffect, useRef, useState } from "react"
import { useNavigate } from "react-router-dom"
import { EmptyState } from "@/components/empty-state"
import { toastError } from "@/components/toast"
import { Button } from "@/components/ui/button"
import { type TranslationKeys, useI18n } from "@/i18n"
import { type NotificationItem, portal } from "@/lib/api"
import { formatMinor, formatMinorUnits } from "@/lib/money"
import { cn, formatRelativeTime } from "@/lib/utils"

type Translate = (key: TranslationKeys, params?: Record<string, string | number>) => string

// humanizeEvent turns an event or data key into readable words:
// "license.payment_failed" → "License payment failed". It is the
// fallback for any key the i18n catalog does not carry, so a new
// server-side event degrades to a readable line, never a raw key.
export function humanizeEvent(event: string): string {
  const words = event.replace(/[._-]+/g, " ").trim()
  return words.charAt(0).toUpperCase() + words.slice(1)
}

// notificationTitle renders a row's text from its title_key (the
// event name). The 28 platform events have notifications.events.*
// keys in every locale; anything else humanizes.
export function notificationTitle(t: Translate, n: NotificationItem): string {
  const key = `notifications.events.${n.title_key || n.event}` as TranslationKeys
  const text = t(key)
  return text === key ? humanizeEvent(n.title_key || n.event) : text
}

// notificationChips flattens the row's data payload into display
// chips. *_minor (and a bare `amount`) are integer minor-unit money —
// formatted with the row's own currency when it carries one, and
// digit-grouped otherwise so a ledger figure still reads.
export function notificationChips(n: NotificationItem): { label: string; value: string }[] {
  const data = n.data || {}
  const currency = typeof data.currency === "string" ? data.currency : ""
  const chips: { label: string; value: string }[] = []
  for (const [k, v] of Object.entries(data)) {
    if (k === "currency" || v === null || v === undefined || v === "") continue
    let value: string
    if (typeof v === "number") {
      const money = k.endsWith("_minor") || k === "amount"
      value = money && currency ? formatMinor(v, currency) : formatMinorUnits(v)
    } else {
      value = String(v)
    }
    chips.push({ label: humanizeEvent(k), value })
  }
  return chips
}

// Priority is styling, not an enum: high/urgent rows stand out, low
// rows sit back, and a value the server invents later renders as a
// plain row instead of breaking.
function priorityClass(priority: string): string {
  switch (priority) {
    case "urgent":
    case "high":
      return "border-l-4 border-l-red-400 bg-red-50/50"
    case "low":
      return "opacity-70"
    default:
      return ""
  }
}

// NotificationRow is the one row shape: title, data chips, relative
// timestamp, unread styling. Clicking it runs onOpen (mark read +
// follow the deep link); onMarkRead, when given, adds an explicit
// "mark as read" button for rows the reader wants to keep in place.
export function NotificationRow({
  notification,
  onOpen,
  onMarkRead,
}: {
  notification: NotificationItem
  onOpen: (n: NotificationItem) => void
  onMarkRead?: (n: NotificationItem) => void
}) {
  const { t, locale } = useI18n()
  const n = notification
  const unread = !n.read_at
  const chips = notificationChips(n)
  return (
    <div
      className={cn(
        "flex items-start gap-2 rounded-md border px-3 py-2.5",
        !unread && "opacity-75",
        priorityClass(n.priority),
      )}
    >
      <button type="button" onClick={() => onOpen(n)} className="min-w-0 flex-1 text-left">
        <div className="flex items-center gap-2">
          {unread && <span className="h-2 w-2 shrink-0 rounded-full bg-primary" aria-hidden="true" />}
          <span className={cn("text-sm", unread && "font-medium")}>{notificationTitle(t, n)}</span>
        </div>
        {chips.length > 0 && (
          <div className="mt-1 flex flex-wrap gap-1">
            {chips.map((c) => (
              <span key={c.label} className="rounded bg-muted px-1.5 py-0.5 text-xs text-muted-foreground">
                {c.label}: {c.value}
              </span>
            ))}
          </div>
        )}
        <div className="mt-1 text-xs text-muted-foreground">{formatRelativeTime(n.created_at, locale)}</div>
      </button>
      {onMarkRead && unread && (
        <Button variant="ghost" size="sm" className="shrink-0 text-xs" onClick={() => onMarkRead(n)}>
          {t("notifications.markRead")}
        </Button>
      )}
    </div>
  )
}

// NotificationBell — the header bell. Badge shows the unread count
// (the list response carries it beside the rows); the panel holds the
// ten most recent rows, "mark all read", and a link to the full page.
export function NotificationBell() {
  const { t } = useI18n()
  const navigate = useNavigate()
  const qc = useQueryClient()
  const [open, setOpen] = useState(false)
  const rootRef = useRef<HTMLDivElement>(null)

  // Badge and panel share one call. Refetch when the window regains
  // focus: the simplest poll that keeps the badge honest without
  // standing up a websocket (plan §88).
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "notifications", "recent"],
    queryFn: () => portal.listNotifications({ limit: 10 }),
    refetchOnWindowFocus: true,
    staleTime: 15_000,
  })
  const notifications = data?.notifications ?? []
  const unread = data?.unread_count ?? 0

  // Close on outside click / Escape — a panel left open over the page
  // it just navigated away from is worse than no panel.
  useEffect(() => {
    if (!open) return
    const onPointerDown = (e: MouseEvent) => {
      if (rootRef.current && !rootRef.current.contains(e.target as Node)) setOpen(false)
    }
    const onKeyDown = (e: KeyboardEvent) => {
      if (e.key === "Escape") setOpen(false)
    }
    document.addEventListener("mousedown", onPointerDown)
    document.addEventListener("keydown", onKeyDown)
    return () => {
      document.removeEventListener("mousedown", onPointerDown)
      document.removeEventListener("keydown", onKeyDown)
    }
  }, [open])

  const invalidate = () => qc.invalidateQueries({ queryKey: ["portal", "notifications"] })
  const readMut = useMutation({
    mutationFn: (id: string) => portal.markNotificationRead(id),
    onSuccess: invalidate,
    onError: (e: Error) => toastError(e),
  })
  const readAllMut = useMutation({
    mutationFn: () => portal.markAllNotificationsRead(),
    onSuccess: invalidate,
    onError: (e: Error) => toastError(e),
  })

  // Click-through marks the row read and follows its deep link. A row
  // without a link only marks read.
  const openRow = (n: NotificationItem) => {
    if (!n.read_at) readMut.mutate(n.id)
    setOpen(false)
    if (n.link) navigate(n.link)
  }

  return (
    <div ref={rootRef} className="relative">
      <Button
        variant="ghost"
        size="icon"
        className="relative"
        aria-label={unread > 0 ? t("notifications.bellLabel", { count: unread }) : t("notifications.bellLabelNone")}
        onClick={() => setOpen((o) => !o)}
      >
        <Bell className="h-5 w-5" />
        {unread > 0 && (
          <span
            aria-hidden="true"
            className="absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-destructive px-1 text-[10px] font-semibold text-destructive-foreground"
          >
            {unread > 99 ? "99+" : unread}
          </span>
        )}
      </Button>
      {open && (
        <div className="absolute right-0 top-full z-50 mt-2 w-[min(22rem,calc(100vw-2rem))] rounded-lg border bg-card shadow-lg">
          <div className="flex items-center justify-between gap-2 border-b px-3 py-2">
            <span className="text-sm font-semibold">{t("notifications.title")}</span>
            <Button
              variant="ghost"
              size="sm"
              className="h-7 gap-1 px-2 text-xs"
              disabled={unread === 0 || readAllMut.isPending}
              onClick={() => readAllMut.mutate()}
            >
              <CheckCheck className="h-3.5 w-3.5" /> {t("notifications.markAllRead")}
            </Button>
          </div>
          <div className="max-h-80 space-y-1.5 overflow-y-auto p-2">
            {isLoading && notifications.length === 0 ? (
              <div className="h-24 animate-pulse rounded-md bg-muted" />
            ) : notifications.length === 0 ? (
              <EmptyState icon={BellOff} title={t("notifications.empty")} description={t("notifications.emptyDesc")} />
            ) : (
              notifications.map((n) => <NotificationRow key={n.id} notification={n} onOpen={openRow} />)
            )}
          </div>
          <div className="border-t p-2">
            <Button
              variant="outline"
              size="sm"
              className="w-full"
              onClick={() => {
                setOpen(false)
                navigate("/portal/notifications")
              }}
            >
              {t("notifications.viewAll")}
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}
