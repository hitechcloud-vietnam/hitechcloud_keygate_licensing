import { useQuery } from "@tanstack/react-query"
import { useI18n } from "@/i18n"
import { portal } from "@/lib/api"
import { cn } from "@/lib/utils"

// A single metered feature's usage against its quota, read live from
// POST /portal/usage/status. Usage counts are integers (units consumed),
// not money — only currency amounts must avoid float math; the ratio
// here is purely a progress-bar width.
//
// limit <= 0 means "no quota" on the backend (remaining reads -1), so we
// show "used / Unlimited" and never a 100%-full bar for it.
export function UsageBar({
  licenseKey,
  feature,
  limit,
  unit,
  period,
}: {
  licenseKey: string
  feature: string
  limit: number
  unit?: string
  period?: string
}) {
  const { t } = useI18n()
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "quota", licenseKey, feature],
    queryFn: () => portal.quotaStatus({ license_key: licenseKey, feature }),
  })

  const used = data?.used ?? 0
  const unlimited = limit <= 0
  const pct = unlimited ? 0 : Math.min((used / limit) * 100, 100)
  const warning = !unlimited && pct >= 80

  return (
    <div className="space-y-1.5">
      <div className="flex items-center justify-between text-sm">
        <span className="font-medium">{feature}</span>
        <span className={cn("text-xs", warning ? "text-amber-600 font-medium" : "text-muted-foreground")}>
          {isLoading
            ? t("common.loading")
            : t("portal.usageUsedOf", {
                used: used.toLocaleString(),
                limit: unlimited ? t("portal.usageUnlimited") : limit.toLocaleString(),
              })}
        </span>
      </div>
      <div className="h-2.5 bg-muted rounded-full overflow-hidden">
        <div
          className={cn(
            "h-full rounded-full transition-all",
            warning ? (pct >= 95 ? "bg-red-500" : "bg-amber-500") : "bg-emerald-500",
          )}
          style={{ width: `${pct}%` }}
        />
      </div>
      {unit && period && <p className="text-xs text-muted-foreground">{t("portal.usagePer", { unit, period })}</p>}
    </div>
  )
}
