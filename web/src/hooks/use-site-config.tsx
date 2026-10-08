import { createContext, type ReactNode, useContext, useEffect, useState } from "react"
import type { SitePaymentMethod, SiteSurfaces } from "@/lib/api"
import { site } from "@/lib/api"
import { setActiveSurfaces } from "@/lib/surface"

interface SiteConfig {
  site_name: string
  brand_color: string
  logo_url: string
  timezone: string
  language: string
  attribution_text: string
  attribution_url: string
  // The 5-domain split (optional — absent = single-host mode) and the
  // enabled payment gateways, both from the extended site config.
  surfaces: SiteSurfaces
  payment_methods: SitePaymentMethod[]
  loading: boolean
}

const defaults: SiteConfig = {
  site_name: "HiTechCloud Software License & Commerce Platform",
  brand_color: "",
  logo_url: "",
  timezone: "UTC",
  language: "",
  attribution_text: "Powered by Keygate",
  attribution_url: "https://keygate.app",
  surfaces: {},
  payment_methods: [],
  loading: true,
}

const SiteConfigContext = createContext<SiteConfig>(defaults)

export function SiteConfigProvider({ children }: { children: ReactNode }) {
  const [config, setConfig] = useState<SiteConfig>(defaults)

  useEffect(() => {
    site
      .config()
      .then((data) => {
        const surfaces = data.surfaces || {}
        setConfig({
          site_name: data.site_name || "HiTechCloud Software License & Commerce Platform",
          brand_color: data.brand_color || "",
          logo_url: data.logo_url || "",
          timezone: data.timezone || "UTC",
          language: data.language || "",
          attribution_text: data.attribution_text || "Powered by Keygate",
          attribution_url: data.attribution_url || "https://keygate.app",
          surfaces,
          payment_methods: data.payment_methods || [],
          loading: false,
        })
        // Surface-aware routing reads the map without prop-threading;
        // single-host installs register an empty map and stay inert.
        setActiveSurfaces(surfaces)
        // Dynamic favicon from custom logo. index.html declares
        // multiple <link rel="icon"> variants and browsers pick their
        // favorite (often the sizes="32x32" one), so rewriting only
        // the first link never visibly changed the tab icon — update
        // them all.
        const logoURL = data.logo_url
        if (logoURL) {
          document.querySelectorAll<HTMLLinkElement>("link[rel~='icon']").forEach((link) => {
            link.href = logoURL
          })
        }
        if (data.brand_color) {
          document.documentElement.style.setProperty("--color-primary", data.brand_color)
        }
        if (data.site_name) {
          document.title = data.site_name
        }
        // Set default language if user hasn't explicitly chosen one
        if (data.language && !localStorage.getItem("hitechcloud_locale")) {
          localStorage.setItem("hitechcloud_locale", data.language)
          document.documentElement.lang = data.language
        }
      })
      .catch(() => setConfig({ ...defaults, loading: false }))
  }, [])

  return <SiteConfigContext.Provider value={config}>{children}</SiteConfigContext.Provider>
}

export function useSiteConfig() {
  return useContext(SiteConfigContext)
}
