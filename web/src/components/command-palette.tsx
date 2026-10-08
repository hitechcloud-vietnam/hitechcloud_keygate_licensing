import { useQuery } from "@tanstack/react-query"
import {
  CreditCard,
  FileText,
  Handshake,
  Key,
  Megaphone,
  MonitorSmartphone,
  Package,
  Plus,
  Receipt,
  Search,
  Settings,
  ShoppingCart,
  Users,
} from "lucide-react"
import { type ComponentType, useCallback, useEffect, useMemo, useRef, useState } from "react"
import { useNavigate } from "react-router-dom"
import { Dialog, DialogContent, DialogDescription, DialogHeader, DialogTitle } from "@/components/ui/dialog"
import { useAuth } from "@/hooks/use-auth"
import { type TranslationKeys, useI18n } from "@/i18n"
import type { AdminSearchResult } from "@/lib/api"
import { admin } from "@/lib/api"
import { useDebounced } from "@/lib/use-debounced"

// CommandPalette (plan §87) — Cmd/Ctrl+K anywhere in the admin and
// portal shells. Two halves in one dialog: static navigation actions,
// and global search (plan §86) over the backend's /admin/search, one
// request per pause in typing (250ms debounce).
//
// Accessibility: Radix Dialog owns the focus trap, Escape-to-close and
// focus return, and dialog.tsx pins role="dialog" + aria-modal. The
// input is a combobox over a listbox of options — aria-activedescendant
// points at the highlighted row, ↑/↓ move it, Enter runs it.

type PaletteItem = {
  id: string
  label: string
  hint?: string
  icon: ComponentType<{ className?: string }>
  group: string
  run: () => void
}

const SEARCH_ICONS: Record<string, ComponentType<{ className?: string }>> = {
  product: Package,
  customer: Users,
  order: ShoppingCart,
  invoice: Receipt,
  license: Key,
  subscription: CreditCard,
  device: MonitorSmartphone,
  reseller: Handshake,
  affiliate: Megaphone,
}

// useCommandPaletteHotkey opens the palette on Cmd/Ctrl+K. Mounted by
// the shells that show the search affordance, so the shortcut and the
// button can never disagree about which pages have a palette.
export function useCommandPaletteHotkey(setOpen: (open: boolean) => void): void {
  useEffect(() => {
    const onKey = (e: KeyboardEvent) => {
      if ((e.metaKey || e.ctrlKey) && !e.altKey && !e.shiftKey && e.key.toLowerCase() === "k") {
        e.preventDefault()
        setOpen(true)
      }
    }
    window.addEventListener("keydown", onKey)
    return () => window.removeEventListener("keydown", onKey)
  }, [setOpen])
}

export function CommandPalette({ open, onOpenChange }: { open: boolean; onOpenChange: (open: boolean) => void }) {
  const { t } = useI18n()
  const { user } = useAuth()
  const navigate = useNavigate()
  const [q, setQ] = useState("")
  const [active, setActive] = useState(0)
  const inputRef = useRef<HTMLInputElement>(null)
  const debouncedQ = useDebounced(q.trim(), 250)

  const isAdmin = user?.is_admin === true

  // Global search — admins only. The endpoint sits behind the admin
  // group; asking it from a customer session would only produce 403
  // noise where the static actions already answer the need.
  const search = useQuery({
    queryKey: ["admin-search", debouncedQ],
    enabled: open && isAdmin && debouncedQ !== "",
    queryFn: () => admin.search({ q: debouncedQ, limit: 20 }),
    retry: false,
    staleTime: 15_000,
  })

  const close = useCallback(
    (then?: () => void) => {
      onOpenChange(false)
      setQ("")
      setActive(0)
      then?.()
    },
    [onOpenChange],
  )

  const go = useCallback((to: string) => close(() => navigate(to)), [close, navigate])

  // The static actions. Admin sessions get the admin jumps named in
  // the plan; a customer session gets the portal's own — a palette
  // that offers "Create product" to a buyer is offering a wall.
  const actions: PaletteItem[] = useMemo(() => {
    const adminActions: PaletteItem[] = [
      {
        id: "act-product",
        label: t("palette.createProduct"),
        icon: Plus,
        group: t("palette.actions"),
        run: () => go("/admin/products"),
      },
      {
        id: "act-license",
        label: t("palette.createLicense"),
        icon: Plus,
        group: t("palette.actions"),
        run: () => go("/admin/licenses"),
      },
      {
        id: "act-customer",
        label: t("palette.findCustomer"),
        icon: Users,
        group: t("palette.actions"),
        run: () => go("/admin/customers"),
      },
      {
        id: "act-order",
        label: t("palette.findOrder"),
        icon: ShoppingCart,
        group: t("palette.actions"),
        run: () => go("/admin/orders"),
      },
      {
        id: "act-settings",
        label: t("palette.openSettings"),
        icon: Settings,
        group: t("palette.actions"),
        run: () => go("/admin/settings"),
      },
      {
        id: "act-config",
        label: t("palette.openConfig"),
        icon: FileText,
        group: t("palette.actions"),
        run: () => go("/admin/config"),
      },
    ]
    const portalActions: PaletteItem[] = [
      {
        id: "act-licenses",
        label: t("nav.licenses"),
        icon: Key,
        group: t("palette.actions"),
        run: () => go("/portal/licenses"),
      },
      {
        id: "act-orders",
        label: t("nav.orders"),
        icon: ShoppingCart,
        group: t("palette.actions"),
        run: () => go("/portal/orders"),
      },
      {
        id: "act-account",
        label: t("nav.settings"),
        icon: Settings,
        group: t("palette.actions"),
        run: () => go("/portal/account"),
      },
    ]
    return isAdmin ? adminActions : portalActions
  }, [t, isAdmin, go])

  // The result rows, in the order the server answered (already merged
  // round-robin across types) but labelled by type group.
  const results: PaletteItem[] = useMemo(() => {
    const rows: AdminSearchResult[] = search.data?.results ?? []
    return rows.map((r) => ({
      id: `res-${r.type}-${r.id}`,
      label: r.title || r.subtitle || r.id,
      hint: r.subtitle && r.title ? r.subtitle : undefined,
      icon: SEARCH_ICONS[r.type] ?? Search,
      group: typeLabel(t, r.type),
      run: () => go(r.url),
    }))
  }, [search.data, t, go])

  // Actions lead while the query is empty; once there is a query the
  // server's answers lead and the actions tuck in behind, filtered to
  // the ones that still read as commands for what was typed.
  const matchingActions = useMemo(() => {
    if (debouncedQ === "") return actions
    const needle = debouncedQ.toLowerCase()
    return actions.filter((a) => a.label.toLowerCase().includes(needle))
  }, [actions, debouncedQ])
  const items = [...results, ...matchingActions]

  useEffect(() => {
    if (active > items.length - 1) setActive(items.length - 1)
  }, [items.length, active])

  // Focus the input when the dialog opens — the palette is typed
  // into, always, and a click on the header icon must not land focus
  // on the close button behind it.
  useEffect(() => {
    if (open) inputRef.current?.focus()
  }, [open])

  const activeId = items[active] ? `palette-item-${items[active].id}` : undefined

  return (
    <Dialog open={open} onOpenChange={onOpenChange}>
      <DialogContent className="top-[20%] max-w-xl translate-y-0 p-0">
        <DialogHeader className="px-4 pt-4">
          <DialogTitle>{t("palette.title")}</DialogTitle>
          <DialogDescription className="sr-only">{t("palette.description")}</DialogDescription>
        </DialogHeader>
        <div className="relative border-b">
          <Search className="pointer-events-none absolute left-3 top-1/2 h-4 w-4 -translate-y-1/2 text-muted-foreground" />
          <input
            ref={inputRef}
            role="combobox"
            aria-expanded="true"
            aria-controls="palette-results"
            aria-activedescendant={activeId}
            aria-label={t("palette.placeholder")}
            className="h-12 w-full bg-transparent pl-9 pr-3 text-sm outline-none placeholder:text-muted-foreground"
            placeholder={t("palette.placeholder")}
            value={q}
            onChange={(e) => {
              setQ(e.target.value)
              // A new query starts the highlight at the top again.
              setActive(0)
            }}
            onKeyDown={(e) => {
              if (e.key === "ArrowDown") {
                e.preventDefault()
                setActive((i) => (items.length ? (i + 1) % items.length : 0))
              } else if (e.key === "ArrowUp") {
                e.preventDefault()
                setActive((i) => (items.length ? (i - 1 + items.length) % items.length : 0))
              } else if (e.key === "Enter") {
                e.preventDefault()
                items[active]?.run()
              }
            }}
          />
        </div>
        <div
          id="palette-results"
          role="listbox"
          aria-label={t("palette.resultsLabel")}
          className="max-h-[50vh] overflow-y-auto px-2 pb-2"
        >
          {items.length === 0 ? (
            <p className="px-3 py-8 text-center text-sm text-muted-foreground">
              {debouncedQ === "" ? t("palette.emptyHint") : t("palette.noResults")}
            </p>
          ) : (
            items.map((item, idx) => (
              <PaletteRow
                key={item.id}
                id={`palette-item-${item.id}`}
                item={item}
                active={idx === active}
                onHover={() => setActive(idx)}
                onPick={item.run}
                showGroup={idx === 0 || items[idx - 1].group !== item.group}
              />
            ))
          )}
          {search.isError && debouncedQ !== "" && (
            <p className="px-3 py-2 text-center text-xs text-muted-foreground">{t("palette.searchUnavailable")}</p>
          )}
        </div>
        <div className="flex items-center gap-3 border-t px-4 py-2 text-xs text-muted-foreground">
          <span>
            <kbd className="rounded border bg-muted px-1.5 py-0.5 font-mono">↑</kbd>{" "}
            <kbd className="rounded border bg-muted px-1.5 py-0.5 font-mono">↓</kbd> {t("palette.hintMove")}
          </span>
          <span>
            <kbd className="rounded border bg-muted px-1.5 py-0.5 font-mono">↵</kbd> {t("palette.hintSelect")}
          </span>
          <span>
            <kbd className="rounded border bg-muted px-1.5 py-0.5 font-mono">Esc</kbd> {t("palette.hintClose")}
          </span>
        </div>
      </DialogContent>
    </Dialog>
  )
}

function PaletteRow({
  id,
  item,
  active,
  onHover,
  onPick,
  showGroup,
}: {
  id: string
  item: PaletteItem
  active: boolean
  onHover: () => void
  onPick: () => void
  showGroup: boolean
}) {
  return (
    <div>
      {showGroup && (
        <div className="px-3 pb-1 pt-3 text-xs font-semibold uppercase tracking-wider text-muted-foreground">
          {item.group}
        </div>
      )}
      {/* A real button: keyboard-operable and focusable on its own,
          on top of the combobox's arrow/Enter handling. */}
      <button
        type="button"
        id={id}
        role="option"
        aria-selected={active}
        onMouseMove={onHover}
        onFocus={onHover}
        onClick={onPick}
        className={`flex w-full cursor-pointer items-center gap-3 rounded-md px-3 py-2 text-left text-sm ${
          active ? "bg-accent text-accent-foreground" : "text-foreground"
        }`}
      >
        <item.icon className="h-4 w-4 shrink-0 text-muted-foreground" />
        <span className="min-w-0 flex-1 truncate">{item.label}</span>
        {item.hint && <span className="min-w-0 max-w-[45%] truncate text-xs text-muted-foreground">{item.hint}</span>}
      </button>
    </div>
  )
}

// typeLabel names a search result type in the UI language. The type
// strings are the pinned API vocabulary and never translated; the
// labels are.
function typeLabel(t: (key: TranslationKeys) => string, type: string): string {
  switch (type) {
    case "product":
      return t("search.typeProduct")
    case "customer":
      return t("search.typeCustomer")
    case "order":
      return t("search.typeOrder")
    case "invoice":
      return t("search.typeInvoice")
    case "license":
      return t("search.typeLicense")
    case "subscription":
      return t("search.typeSubscription")
    case "device":
      return t("search.typeDevice")
    case "reseller":
      return t("search.typeReseller")
    case "affiliate":
      return t("search.typeAffiliate")
    default:
      return type
  }
}
