// Reports (plan §43) — the admin reporting surface over Agent M's
// reporting engine. One run answers {series, totals}: series rows
// carry `period` for time series and the resellers / affiliates
// reports answer with a ranked top list instead. Metric keys are
// self-describing — *_count is an integer count, *_minor is integer
// minor-unit money — so columns are derived from the keys and
// humanized ("revenue_minor" → "Revenue"). No chart library: the data
// table IS the visualization, with totals in the footer.
import { useQuery } from "@tanstack/react-query"
import { Download, FileText } from "lucide-react"
import { useMemo, useState } from "react"
import { EmptyState } from "@/components/empty-state"
import { toastError } from "@/components/toast"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import {
  DataTable,
  DataTableBody,
  DataTableCell,
  DataTableHead,
  DataTableHeader,
  DataTableRow,
} from "@/components/ui/data-table"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import {
  Select,
  SelectContent,
  SelectGroup,
  SelectItem,
  SelectLabel,
  SelectTrigger,
  SelectValue,
} from "@/components/ui/select"
import { type TranslationKeys, useI18n } from "@/i18n"
import { admin, type ReportRow } from "@/lib/api"
import { formatMinor, formatMinorUnits } from "@/lib/money"

// The 14 report types (plan §43), organized for the picker. The
// catalog response drives what is offered; these groups only bundle
// the known types — anything the server adds lands under "Other".
const REPORT_GROUPS: { label: TranslationKeys; types: string[] }[] = [
  { label: "reports.groupCommerce", types: ["revenue", "sales", "refunds", "failed_payments"] },
  { label: "reports.groupSubscriptions", types: ["subscriptions", "renewals", "churn"] },
  { label: "reports.groupLicensing", types: ["licenses", "activations", "devices", "usage"] },
  { label: "reports.groupPartners", types: ["customers", "resellers", "affiliates"] },
]
const FALLBACK_TYPES = REPORT_GROUPS.flatMap((g) => g.types)
const FALLBACK_GROUP_BY = ["day", "week", "month"]

// These two answer with a ranked top list (no `period` in the rows),
// so they get a rank column instead of a period column.
const TOP_LIST_TYPES = new Set(["resellers", "affiliates"])

const GROUP_BY_LABEL: Record<string, TranslationKeys> = {
  day: "reports.groupByDay",
  week: "reports.groupByWeek",
  month: "reports.groupByMonth",
}

const RANGE_PRESETS = ["7", "30", "90", "custom"] as const

// isoDay formats a local date as YYYY-MM-DD — the shape the API's
// from/to take. Built from local parts so the range the admin sees is
// the range the report runs, not a UTC one shifted by the timezone.
function isoDay(d: Date): string {
  const y = d.getFullYear()
  const m = String(d.getMonth() + 1).padStart(2, "0")
  const day = String(d.getDate()).padStart(2, "0")
  return `${y}-${m}-${day}`
}

// metricLabel humanizes a metric key into its column header:
// "revenue_minor" → "Revenue", "failed_payments_count" → "Failed
// payments", "reseller_id" → "Reseller". The suffix is a unit or a
// key column marker, not part of the name — the unit is carried by
// how the cell is formatted.
function metricLabel(key: string): string {
  const noun = key.replace(/_(minor|count|bps|id)$/, "")
  return noun
    .split("_")
    .map((w) => w.charAt(0).toUpperCase() + w.slice(1))
    .join(" ")
}

function isMoneyMetric(key: string): boolean {
  return key.endsWith("_minor")
}

function formatMetric(key: string, value: string | number | null | undefined, currency: string): string {
  if (value === null || value === undefined) return "–"
  if (typeof value === "string") return value
  if (isMoneyMetric(key)) return currency ? formatMinor(value, currency) : formatMinorUnits(value)
  return formatMinorUnits(value)
}

export default function ReportsPage() {
  const { t } = useI18n()
  const [type, setType] = useState("revenue")
  const [preset, setPreset] = useState<string>("30")
  const [customFrom, setCustomFrom] = useState("")
  const [customTo, setCustomTo] = useState("")
  const [groupBy, setGroupBy] = useState("day")
  const [downloading, setDownloading] = useState(false)

  // A preset range always ends today; "custom" hands both ends to the
  // admin's date inputs. An empty custom end passes no bound at all
  // (the server's own default) rather than an empty string.
  const { from, to } = useMemo(() => {
    if (preset === "custom") return { from: customFrom, to: customTo }
    const end = new Date()
    const start = new Date()
    start.setDate(end.getDate() - (Number(preset) - 1))
    return { from: isoDay(start), to: isoDay(end) }
  }, [preset, customFrom, customTo])

  const { data: catalog } = useQuery({
    queryKey: ["admin", "reports", "catalog"],
    queryFn: () => admin.listReportTypes(),
    staleTime: 5 * 60_000,
  })
  const reportTypes = catalog?.report_types?.length ? catalog.report_types : FALLBACK_TYPES
  const groupByOptions = catalog?.group_by?.length ? catalog.group_by : FALLBACK_GROUP_BY

  const { data, isLoading, isError, refetch } = useQuery({
    queryKey: ["admin", "reports", type, from, to, groupBy],
    queryFn: () => admin.getReport(type, { from: from || undefined, to: to || undefined, group_by: groupBy }),
  })

  const rows: ReportRow[] = data?.series ?? []
  const totals = data?.totals ?? {}
  const isTopList = TOP_LIST_TYPES.has(type)

  // Column order: metric keys in the order the server listed them —
  // first row wins, later rows only add columns they alone carry.
  // `period` is the row identity, `currency` is context; neither is a
  // metric column.
  const columns = useMemo(() => {
    const keys: string[] = []
    const seen = new Set<string>()
    const add = (k: string) => {
      if (k !== "period" && k !== "currency" && !seen.has(k)) {
        seen.add(k)
        keys.push(k)
      }
    }
    for (const r of rows) for (const k of Object.keys(r)) add(k)
    for (const k of Object.keys(totals)) add(k)
    return keys
  }, [rows, totals])

  // When the rows name a currency the money columns can be formatted
  // properly; without one they are digit-grouped minor units and say
  // so under the table.
  const firstCurrency = rows[0]?.currency
  const currency = typeof firstCurrency === "string" ? firstCurrency : ""
  const hasTotals = Object.keys(totals).length > 0
  const hasMoney = columns.some(isMoneyMetric)

  const typeLabel = (reportType: string): string => {
    const key = `reports.types.${reportType}` as TranslationKeys
    const text = t(key)
    return text === key ? metricLabel(reportType) : text
  }

  const groupedTypes = () => {
    const used = new Set<string>()
    const groups = REPORT_GROUPS.map((g) => {
      const items = reportTypes.filter((x) => g.types.includes(x))
      for (const x of items) used.add(x)
      return { label: g.label, items }
    }).filter((g) => g.items.length > 0)
    const rest = reportTypes.filter((x) => !used.has(x))
    if (rest.length > 0) groups.push({ label: "reports.groupOther" as TranslationKeys, items: rest })
    return groups
  }

  // CSV export: fetch the file (session cookie carries the auth) and
  // hand it to an anchor so the browser saves it under the pinned
  // <type>-<from>-<to>.csv name. Failures toast through §90.
  const downloadCsv = async () => {
    setDownloading(true)
    try {
      const blob = await admin.exportReport(type, {
        from: from || undefined,
        to: to || undefined,
        group_by: groupBy,
        format: "csv",
      })
      const url = URL.createObjectURL(blob)
      const a = document.createElement("a")
      a.href = url
      a.download = `${type}-${from || "start"}-${to || "today"}.csv`
      document.body.appendChild(a)
      a.click()
      a.remove()
      URL.revokeObjectURL(url)
    } catch (e) {
      toastError(e)
    } finally {
      setDownloading(false)
    }
  }

  const numericClass = (key: string) => (isMoneyMetric(key) || key.endsWith("_count") ? "text-right tabular-nums" : "")

  return (
    <div className="space-y-6">
      <div className="flex flex-wrap items-center justify-between gap-3">
        <div className="min-w-0">
          <h1 className="text-2xl font-bold tracking-tight sr-only md:not-sr-only">{t("reports.title")}</h1>
          <p className="text-muted-foreground">{t("reports.subtitle")}</p>
        </div>
        <Button variant="outline" onClick={downloadCsv} disabled={downloading || isLoading}>
          <Download className="h-4 w-4 mr-2" />
          {downloading ? t("common.loading") : t("reports.downloadCsv")}
        </Button>
      </div>

      <Card>
        <CardContent className="pt-6">
          <div className="flex flex-wrap items-end gap-3">
            <div className="w-full sm:w-56">
              <Label>{t("reports.reportType")}</Label>
              <Select value={type} onValueChange={setType}>
                <SelectTrigger className="mt-1.5">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {groupedTypes().map((group) => (
                    <SelectGroup key={group.label}>
                      <SelectLabel>{t(group.label)}</SelectLabel>
                      {group.items.map((reportType) => (
                        <SelectItem key={reportType} value={reportType}>
                          {typeLabel(reportType)}
                        </SelectItem>
                      ))}
                    </SelectGroup>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div className="w-full sm:w-40">
              <Label>{t("reports.groupBy")}</Label>
              <Select value={groupBy} onValueChange={setGroupBy}>
                <SelectTrigger className="mt-1.5">
                  <SelectValue />
                </SelectTrigger>
                <SelectContent>
                  {groupByOptions.map((g) => (
                    <SelectItem key={g} value={g}>
                      {GROUP_BY_LABEL[g] ? t(GROUP_BY_LABEL[g]) : metricLabel(g)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
            <div>
              <Label>{t("reports.range")}</Label>
              <div className="mt-1.5 flex flex-wrap gap-1.5">
                {RANGE_PRESETS.map((p) => (
                  <Button
                    key={p}
                    size="sm"
                    variant={preset === p ? "default" : "outline"}
                    onClick={() => setPreset(p)}
                    aria-pressed={preset === p}
                  >
                    {p === "custom" ? t("reports.rangeCustom") : t(`reports.range${p}` as TranslationKeys)}
                  </Button>
                ))}
              </div>
            </div>
            {preset === "custom" && (
              <>
                <div className="w-full sm:w-40">
                  <Label>{t("reports.from")}</Label>
                  <Input
                    type="date"
                    value={customFrom}
                    onChange={(e) => setCustomFrom(e.target.value)}
                    className="mt-1.5"
                  />
                </div>
                <div className="w-full sm:w-40">
                  <Label>{t("reports.to")}</Label>
                  <Input
                    type="date"
                    value={customTo}
                    onChange={(e) => setCustomTo(e.target.value)}
                    className="mt-1.5"
                  />
                </div>
              </>
            )}
          </div>
        </CardContent>
      </Card>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-40 animate-pulse rounded-lg bg-muted" />
          ) : isError ? (
            <EmptyState
              icon={FileText}
              title={t("reports.errorTitle")}
              description={t("error.whatNext")}
              action={{ label: t("common.retry"), onClick: () => refetch() }}
            />
          ) : rows.length === 0 ? (
            <EmptyState icon={FileText} title={t("reports.empty")} description={t("reports.emptyDesc")} />
          ) : (
            <>
              <DataTable>
                <DataTableHeader>
                  <DataTableRow>
                    {isTopList ? (
                      <DataTableHead className="w-12">{t("reports.rank")}</DataTableHead>
                    ) : (
                      <DataTableHead>{t("reports.period")}</DataTableHead>
                    )}
                    {columns.map((k) => (
                      <DataTableHead key={k} className={isMoneyMetric(k) || k.endsWith("_count") ? "text-right" : ""}>
                        {metricLabel(k)}
                      </DataTableHead>
                    ))}
                  </DataTableRow>
                </DataTableHeader>
                <DataTableBody>
                  {rows.map((row, i) => (
                    <DataTableRow key={String(row.period ?? i)}>
                      {isTopList ? (
                        <DataTableCell className="text-muted-foreground">{i + 1}</DataTableCell>
                      ) : (
                        <DataTableCell className="font-medium">{String(row.period ?? "–")}</DataTableCell>
                      )}
                      {columns.map((k) => (
                        <DataTableCell key={k} className={numericClass(k)}>
                          {formatMetric(k, row[k], currency)}
                        </DataTableCell>
                      ))}
                    </DataTableRow>
                  ))}
                </DataTableBody>
                {hasTotals && (
                  <tfoot>
                    <DataTableRow className="border-t bg-muted/40 font-medium">
                      <DataTableCell className="font-semibold">{t("reports.totals")}</DataTableCell>
                      {columns.map((k) => (
                        <DataTableCell key={k} className={numericClass(k)}>
                          {k in totals ? formatMetric(k, totals[k], currency) : ""}
                        </DataTableCell>
                      ))}
                    </DataTableRow>
                  </tfoot>
                )}
              </DataTable>
              {hasMoney && !currency && <p className="mt-3 text-xs text-muted-foreground">{t("reports.moneyNote")}</p>}
            </>
          )}
        </CardContent>
      </Card>
    </div>
  )
}
