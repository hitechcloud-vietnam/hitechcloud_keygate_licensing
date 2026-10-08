import { ExternalLink, PanelsTopLeft } from "lucide-react"
import {
  DropdownMenu,
  DropdownMenuContent,
  DropdownMenuItem,
  DropdownMenuSeparator,
  DropdownMenuTrigger,
} from "@/components/ui/dropdown-menu"
import { useSiteConfig } from "@/hooks/use-site-config"
import { useI18n } from "@/i18n"
import { hasSurfaces, type SurfaceName, surfaceOf, surfaceURL } from "@/lib/surface"

// SurfaceSwitcher — the "switch surface" affordance the portal and
// admin shells show when (and only when) this install runs the
// 5-domain split: links to the surfaces that have a hostname of their
// own, other than the one being viewed. In single-host mode there is
// nothing to switch to and the whole control disappears — the shells
// look exactly as they did before.
//
// Links leave the current origin, so they are plain <a> (a React
// Router Link would soft-navigate and never reach the other host).
export function SurfaceSwitcher() {
  const { t } = useI18n()
  const { surfaces } = useSiteConfig()

  if (!hasSurfaces(surfaces)) return null

  const current = surfaceOf(window.location.hostname, surfaces)
  const entries: { surface: SurfaceName; path: string; label: string }[] = [
    { surface: "dashboard", path: "/admin", label: t("surfaces.dashboard") },
    { surface: "customer", path: "/portal", label: t("surfaces.customerPortal") },
    { surface: "merchant", path: "/portal/reseller", label: t("surfaces.merchantPortal") },
    { surface: "apex", path: "/marketplace", label: t("surfaces.marketplace") },
  ]
  const links = entries.flatMap((e) => {
    if (e.surface === current) return []
    const url = surfaceURL(e.path, e.surface, surfaces)
    return url ? [{ surface: e.surface, label: e.label, url }] : []
  })

  if (links.length === 0) return null

  return (
    <DropdownMenu>
      <DropdownMenuTrigger className="rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring">
        <span className="flex h-9 items-center gap-1.5 rounded-md px-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground">
          <PanelsTopLeft className="h-4 w-4" />
          <span className="hidden sm:inline">{t("surfaces.switch")}</span>
        </span>
      </DropdownMenuTrigger>
      <DropdownMenuContent align="end">
        <div className="px-2 py-1.5 text-xs font-semibold text-muted-foreground">{t("surfaces.switch")}</div>
        <DropdownMenuSeparator />
        {links.map((l) => (
          <DropdownMenuItem key={l.surface} asChild>
            <a href={l.url} className="flex items-center gap-2">
              {l.label}
              <ExternalLink className="h-3.5 w-3.5 opacity-50" />
            </a>
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
