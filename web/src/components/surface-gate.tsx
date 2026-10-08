import { type ReactNode, useEffect, useRef } from "react"
import { useLocation } from "react-router-dom"
import { useSiteConfig } from "@/hooks/use-site-config"
import { surfaceRedirectTarget } from "@/lib/surface"

// SurfaceGate — the app-boot redirect for the 5-domain split. When
// site config names per-surface hostnames and the current ROUTE
// belongs to a different surface than the hostname the visitor is on,
// the browser is sent to that surface's host (same path, same query
// string). Single-host mode — no `surfaces` in the config — renders
// children untouched: zero redirects, every old behaviour preserved.
//
// location.replace, not a React navigation: this is a host change,
// the old page must not stay in history to be backed into.
//
// The search string is preserved deliberately: checkout hand-offs
// encode the cart's items in it, and localStorage does not follow the
// browser to another hostname.
export function SurfaceGate({ children }: { children: ReactNode }) {
  const { surfaces, loading } = useSiteConfig()
  const location = useLocation()
  const redirected = useRef(false)

  useEffect(() => {
    if (loading || redirected.current) return
    const target = surfaceRedirectTarget(window.location.hostname, location.pathname, location.search, surfaces)
    if (target) {
      redirected.current = true
      window.location.replace(target)
    }
  }, [loading, surfaces, location.pathname, location.search])

  return <>{children}</>
}
