import { useQuery } from "@tanstack/react-query"
import {
  Activity,
  CreditCard,
  Download,
  FileKey2,
  Gauge,
  Key,
  LayoutDashboard,
  LifeBuoy,
  Link2,
  MonitorSmartphone,
  ShoppingCart,
  User,
} from "lucide-react"
import { Link } from "react-router-dom"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { UsageBar } from "@/components/usage-bar"
import { useAuth } from "@/hooks/use-auth"
import { type TranslationKeys, useI18n } from "@/i18n"
import type { Entitlement, PortalLicense } from "@/lib/api"
import { portal } from "@/lib/api"
import { formatMinor } from "@/lib/money"
import { formatDate, statusColor } from "@/lib/utils"

// A quota-bearing entitlement on a SaaS/hybrid licence. Usage is metered
// per feature; the snapshot shows each feature against its quota.
function quotaEntitlements(lic: PortalLicense): Entitlement[] {
  return (lic.plan?.entitlements || []).filter((e) => e.value_type === "quota")
}

function isMetered(lic: PortalLicense): boolean {
  const t = lic.product?.type || "perpetual"
  return (t === "saas" || t === "hybrid") && quotaEntitlements(lic).length > 0
}

export default function PortalDashboardPage() {
  const { t } = useI18n()
  const { user } = useAuth()
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "licenses"],
    queryFn: portal.licenses,
  })
  const ordersQuery = useQuery({
    queryKey: ["portal", "orders", "dashboard"],
    queryFn: () => portal.listPortalOrders({ limit: 5 }),
  })

  const licenses = data?.licenses || []
  const activeLicenses = licenses.filter((l) => l.status === "active" || l.status === "trialing")
  const activeSubscriptions = activeLicenses.filter((l) => !!l.stripe_subscription_id)
  const activeDevices = licenses.reduce((sum, l) => sum + (l.activation_count ?? l.activations?.length ?? 0), 0)
  const recentOrders = ordersQuery.data?.orders || []
  const totalOrders = ordersQuery.data?.total ?? recentOrders.length
  // The first metered licence drives the usage snapshot; the Usage page
  // shows every licence in full. Bounded so the dashboard stays cheap.
  const metered = licenses.find(isMetered)
  const meteredFeatures = metered ? quotaEntitlements(metered).slice(0, 3) : []

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">
          {t("portal.dashWelcome", { name: user?.name || user?.email || "" })}
        </h1>
        <p className="text-muted-foreground">{t("portal.dashSubtitle")}</p>
      </div>

      {/* Summary cards */}
      <div className="grid gap-4 sm:grid-cols-2 lg:grid-cols-4">
        <StatCard
          to="/portal/licenses"
          icon={Key}
          label={t("portal.dashActiveLicenses")}
          value={isLoading ? "…" : String(activeLicenses.length)}
        />
        <StatCard
          to="/portal/subscriptions"
          icon={CreditCard}
          label={t("portal.dashActiveSubscriptions")}
          value={isLoading ? "…" : String(activeSubscriptions.length)}
        />
        <StatCard
          to="/portal/devices"
          icon={MonitorSmartphone}
          label={t("portal.activeDevices")}
          value={isLoading ? "…" : String(activeDevices)}
        />
        <StatCard
          to="/portal/orders"
          icon={ShoppingCart}
          label={t("nav.orders")}
          value={ordersQuery.isLoading ? "…" : String(totalOrders)}
        />
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        {/* Usage / quota snapshot */}
        <Card>
          <CardHeader>
            <div className="flex items-center justify-between">
              <CardTitle className="flex items-center gap-2 text-lg">
                <Gauge className="h-5 w-5 text-muted-foreground" /> {t("portal.dashUsageSnapshot")}
              </CardTitle>
              <Link to="/portal/usage" className="text-sm text-primary hover:underline">
                {t("portal.dashViewAll")}
              </Link>
            </div>
          </CardHeader>
          <CardContent>
            {!metered || meteredFeatures.length === 0 ? (
              <p className="text-sm text-muted-foreground py-6 text-center">{t("portal.dashNoUsage")}</p>
            ) : (
              <div className="space-y-4">
                <p className="text-xs text-muted-foreground">
                  {metered.product?.name}
                  {metered.plan ? ` · ${metered.plan.name}` : ""}
                </p>
                {meteredFeatures.map((ent) => (
                  <UsageBar
                    key={ent.id}
                    licenseKey={metered.license_key}
                    feature={ent.feature}
                    limit={Number(ent.value) || 0}
                    unit={ent.quota_unit}
                    period={ent.quota_period}
                  />
                ))}
              </div>
            )}
          </CardContent>
        </Card>

        {/* Recent orders */}
        <Card>
          <CardHeader>
            <div className="flex items-center justify-between">
              <CardTitle className="flex items-center gap-2 text-lg">
                <ShoppingCart className="h-5 w-5 text-muted-foreground" /> {t("portal.dashRecentOrders")}
              </CardTitle>
              <Link to="/portal/orders" className="text-sm text-primary hover:underline">
                {t("portal.dashViewAll")}
              </Link>
            </div>
          </CardHeader>
          <CardContent>
            {ordersQuery.isLoading ? (
              <div className="h-24 animate-pulse bg-muted rounded-lg" />
            ) : recentOrders.length === 0 ? (
              <p className="text-sm text-muted-foreground py-6 text-center">{t("portal.dashNoOrders")}</p>
            ) : (
              <div className="space-y-2">
                {recentOrders.map((o) => (
                  <div key={o.id} className="flex items-center justify-between rounded-lg border px-3 py-2 text-sm">
                    <div className="min-w-0">
                      <p className="font-medium truncate">{o.order_number}</p>
                      <p className="text-xs text-muted-foreground">{formatDate(o.created_at)}</p>
                    </div>
                    <div className="flex items-center gap-2 shrink-0">
                      <Badge className={statusColor(o.status)}>{t(`status.${o.status}` as TranslationKeys)}</Badge>
                      <span className="font-medium">{formatMinor(o.total_minor, o.currency)}</span>
                    </div>
                  </div>
                ))}
              </div>
            )}
          </CardContent>
        </Card>
      </div>

      {/* Quick links */}
      <Card>
        <CardHeader>
          <CardTitle className="flex items-center gap-2 text-lg">
            <LayoutDashboard className="h-5 w-5 text-muted-foreground" /> {t("portal.dashQuickLinks")}
          </CardTitle>
        </CardHeader>
        <CardContent>
          <div className="grid gap-3 sm:grid-cols-2 lg:grid-cols-3">
            <QuickLink to="/portal/licenses" icon={Key} label={t("nav.licenses")} />
            <QuickLink to="/portal/subscriptions" icon={CreditCard} label={t("nav.subscriptions")} />
            <QuickLink to="/portal/devices" icon={MonitorSmartphone} label={t("nav.devices")} />
            <QuickLink to="/portal/usage" icon={Activity} label={t("nav.usage")} />
            <QuickLink to="/portal/downloads" icon={Download} label={t("nav.downloads")} />
            <QuickLink to="/portal/api-keys" icon={FileKey2} label={t("nav.apiKeys")} />
            <QuickLink to="/portal/webhooks" icon={Link2} label={t("nav.webhooks")} />
            <QuickLink to="/portal/support" icon={LifeBuoy} label={t("nav.support")} />
            <QuickLink to="/portal/account" icon={User} label={t("nav.settings")} />
          </div>
        </CardContent>
      </Card>
    </div>
  )
}

function StatCard({
  to,
  icon: Icon,
  label,
  value,
}: {
  to: string
  icon: React.ComponentType<{ className?: string }>
  label: string
  value: string
}) {
  return (
    <Link to={to}>
      <Card className="transition-colors hover:bg-accent/40">
        <CardContent className="pt-5 pb-4">
          <div className="flex items-center justify-between">
            <div>
              <p className="text-sm text-muted-foreground">{label}</p>
              <p className="text-2xl font-bold tracking-tight">{value}</p>
            </div>
            <Icon className="h-6 w-6 text-muted-foreground" />
          </div>
        </CardContent>
      </Card>
    </Link>
  )
}

function QuickLink({
  to,
  icon: Icon,
  label,
}: {
  to: string
  icon: React.ComponentType<{ className?: string }>
  label: string
}) {
  return (
    <Link
      to={to}
      className="flex items-center gap-3 rounded-lg border px-3 py-2.5 text-sm font-medium transition-colors hover:bg-accent/40"
    >
      <Icon className="h-4 w-4 text-muted-foreground" />
      {label}
    </Link>
  )
}
