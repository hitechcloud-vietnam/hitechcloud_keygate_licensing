import { useQuery } from "@tanstack/react-query"
import { Gauge } from "lucide-react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { UsageBar } from "@/components/usage-bar"
import { useI18n } from "@/i18n"
import type { Entitlement, PortalLicense } from "@/lib/api"
import { portal } from "@/lib/api"

// Read-only quota view. Usage is read through POST /portal/usage/status
// (a read-shaped query). The sibling POST /portal/usage *records* usage
// (it increments counters and can trip quota webhooks) — it is an ingest
// endpoint and is deliberately never called from this page.

function quotaEntitlements(lic: PortalLicense): Entitlement[] {
  return (lic.plan?.entitlements || []).filter((e) => e.value_type === "quota")
}

function isMetered(lic: PortalLicense): boolean {
  const t = lic.product?.type || "perpetual"
  return (t === "saas" || t === "hybrid") && quotaEntitlements(lic).length > 0
}

export default function PortalUsagePage() {
  const { t } = useI18n()
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "licenses"],
    queryFn: portal.licenses,
  })

  const metered = (data?.licenses || []).filter(isMetered)

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("portal.usageTitle")}</h1>
        <p className="text-muted-foreground">{t("portal.usageDesc")}</p>
      </div>

      {isLoading ? (
        <div className="space-y-4">
          {[1, 2].map((i) => (
            <div key={i} className="h-40 animate-pulse bg-muted rounded-lg" />
          ))}
        </div>
      ) : metered.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center">
            <Gauge className="h-12 w-12 mx-auto text-muted-foreground mb-4" />
            <p className="text-lg font-medium">{t("portal.usageEmpty")}</p>
            <p className="text-muted-foreground mt-1">{t("portal.usageEmptyDesc")}</p>
          </CardContent>
        </Card>
      ) : (
        <div className="space-y-4">
          {metered.map((lic) => (
            <UsageLicenseCard key={lic.id} license={lic} />
          ))}
        </div>
      )}
    </div>
  )
}

function UsageLicenseCard({ license: lic }: { license: PortalLicense }) {
  const { t } = useI18n()
  const features = quotaEntitlements(lic)

  return (
    <Card>
      <CardHeader>
        <div className="flex items-center justify-between">
          <div>
            <CardTitle className="text-lg">{lic.product?.name || t("common.product")}</CardTitle>
            <p className="text-sm text-muted-foreground mt-1">{lic.plan?.name}</p>
          </div>
        </div>
      </CardHeader>
      <CardContent>
        <div className="space-y-5">
          {features.map((ent) => (
            <UsageBar
              key={ent.id}
              licenseKey={lic.license_key}
              feature={ent.feature}
              limit={Number(ent.value) || 0}
              unit={ent.quota_unit}
              period={ent.quota_period}
            />
          ))}
        </div>
      </CardContent>
    </Card>
  )
}
