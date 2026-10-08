// ─── Surface-aware routing (5-domain split, plan §86 shell) ───
//
// One SPA, many hostnames. A "surface" is a hostname that serves one
// slice of the product: payments (checkout), dashboard (/admin),
// merchant (reseller + affiliate portal), customer (customer portal),
// apex (marketing + marketplace), and a handful of non-SPA hosts
// (verify, hooks, docs, status, go, auth, cdn).
//
// The hostname map comes from GET /api/v1/config (`surfaces`, bare
// hostnames, no scheme — see the pinned Surfaces contract). When it
// is empty or absent this install is in SINGLE-HOST mode and every
// helper here is inert: zero redirects, exactly the behaviour the app
// had before the split.
//
// Two rules keep this safe:
//
//   - Redirects preserve <path><search>, so a cart hand-off encoded
//     in the query string survives the hop between hosts (localStorage
//     does NOT survive it — see lib/cart.ts).
//   - A surface with no configured hostname is served by whichever
//     host the visitor is on; nothing redirects to it.

import type { SiteSurfaces } from "@/lib/api"

// SurfaceName is the closed set of surfaces. The SPA-serving ones are
// the first five; the rest exist so surfaceOf can name a hostname it
// is looking at without inventing a value.
export type SurfaceName =
  | "apex"
  | "payments"
  | "dashboard"
  | "merchant"
  | "customer"
  | "verify"
  | "hooks"
  | "docs"
  | "status"
  | "go"
  | "auth"
  | "cdn"

// SURFACE_MATCH_ORDER is the order a hostname is claimed in. First
// match wins, so if one host is named twice (a misconfiguration) the
// more specific surface keeps it.
const SURFACE_MATCH_ORDER: SurfaceName[] = [
  "dashboard",
  "payments",
  "merchant",
  "customer",
  "verify",
  "hooks",
  "docs",
  "status",
  "go",
  "auth",
  "cdn",
  "apex",
]

// ROUTE_SURFACES mirrors the backend's route→surface map, longest
// prefix first. Extensions to the pinned map are marked: /cart and
// /accept-invite are shop/portal pages that must stay (respectively
// arrive) with the surface they belong to.
const ROUTE_SURFACES: [string, SurfaceName][] = [
  ["/checkout", "payments"], // includes /checkout/success + /checkout/gateway-return
  ["/pay", "payments"],
  ["/setup", "dashboard"],
  ["/admin", "dashboard"],
  ["/portal/reseller", "merchant"],
  ["/portal/affiliate", "merchant"],
  ["/portal", "customer"],
  ["/auth", "customer"],
  ["/invites", "customer"],
  ["/accept-invite", "customer"], // invite acceptance is customer-portal business
  ["/marketplace", "apex"],
  ["/pricing", "apex"],
  ["/login", "apex"],
  ["/cart", "apex"], // the storefront's cart lives where the add buttons do
  ["/", "apex"],
]

// normalizeHostname folds a hostname into the form the map and
// location.hostname are compared in: lower-cased, no port, no
// trailing dot. The contract says the map holds bare hostnames; this
// tolerates the operator typing a port or trailing dot anyway.
function normalizeHostname(host: string): string {
  let h = host.trim().toLowerCase()
  h = h.replace(/^https?:\/\//, "")
  h = h.split("/")[0]
  h = h.replace(/:\d+$/, "")
  h = h.replace(/\.$/, "")
  return h
}

function surfaceHost(surface: SurfaceName, surfaces: SiteSurfaces): string {
  const raw = surfaces?.[surface]
  return typeof raw === "string" ? normalizeHostname(raw) : ""
}

// hasSurfaces reports whether any surface hostname is configured —
// the definition of multi-host mode. Empty/absent = single-host.
export function hasSurfaces(surfaces: SiteSurfaces | null | undefined): boolean {
  if (!surfaces) return false
  return SURFACE_MATCH_ORDER.some((s) => surfaceHost(s, surfaces) !== "")
}

// surfaceOf names the surface a hostname serves, or "" when the
// hostname is not in the map (single-host mode, or an operator host
// not named in the split).
export function surfaceOf(hostname: string, surfaces: SiteSurfaces | null | undefined): SurfaceName | "" {
  if (!surfaces) return ""
  const host = normalizeHostname(hostname)
  if (!host) return ""
  for (const name of SURFACE_MATCH_ORDER) {
    const configured = surfaceHost(name, surfaces)
    if (configured !== "" && configured === host) return name
  }
  return ""
}

// canonicalHost is the bare hostname a surface answers on, or "" when
// that surface rides along with the others (not configured).
export function canonicalHost(surface: SurfaceName, surfaces: SiteSurfaces | null | undefined): string {
  if (!surfaces) return ""
  return surfaceHost(surface, surfaces)
}

// routeSurface is the surface a path belongs to, longest prefix wins.
export function routeSurface(pathname: string): SurfaceName {
  for (const [prefix, surface] of ROUTE_SURFACES) {
    if (pathname === prefix || pathname.startsWith(prefix === "/" ? "/" : `${prefix}/`)) return surface
  }
  return "apex"
}

// surfaceURL builds the absolute URL of a path on a surface, or null
// when that surface has no hostname of its own. HTTPS, always: these
// are public hostnames serving a session cookie.
export function surfaceURL(
  path: string,
  surface: SurfaceName,
  surfaces: SiteSurfaces | null | undefined,
): string | null {
  const host = canonicalHost(surface, surfaces)
  if (!host) return null
  return `https://${host}${path.startsWith("/") ? path : `/${path}`}`
}

// The active surface map, set once the site config arrives. This is
// what lets redirectToSurface keep the (path, surface) signature the
// plan calls for without threading the map through every caller.
let activeSurfaces: SiteSurfaces = {}

export function setActiveSurfaces(surfaces: SiteSurfaces | null | undefined): void {
  activeSurfaces = surfaces || {}
}

// redirectToSurface is the target URL of a same-path hop to another
// surface, or null when no hop is needed (single-host mode, the route
// is already on its own surface, or the target surface has no
// hostname). The caller performs the navigation —
// location.replace(url) — so the current page is not kept in history.
export function redirectToSurface(path: string, surface: SurfaceName): string | null {
  const host = canonicalHost(surface, activeSurfaces)
  const current = normalizeHostname(window.location.hostname)
  if (!host || host === current) return null
  return `https://${host}${path.startsWith("/") ? path : `/${path}`}`
}

// surfaceRedirectTarget is the pure decision behind redirectToSurface:
// given where we are, which route we want and the configured map,
// should the browser be sent elsewhere — and to where. The path AND
// search are preserved, so hand-offs encoded in the query survive.
export function surfaceRedirectTarget(
  hostname: string,
  pathname: string,
  search: string,
  surfaces: SiteSurfaces | null | undefined,
): string | null {
  if (!hasSurfaces(surfaces)) return null
  const current = normalizeHostname(hostname)
  const wanted = routeSurface(pathname)
  if (surfaceOf(hostname, surfaces) === wanted) return null
  const host = canonicalHost(wanted, surfaces)
  if (!host || host === current) return null
  return `https://${host}${pathname}${search}`
}
