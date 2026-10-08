import { useQuery } from "@tanstack/react-query"
import { Download } from "lucide-react"
import { ListEmptyState } from "@/components/empty-state"
import { Badge } from "@/components/ui/badge"
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
import { useI18n } from "@/i18n"
import type { PortalDownload } from "@/lib/api"
import { portal } from "@/lib/api"
import { formatDate } from "@/lib/utils"

export default function PortalDownloadsPage() {
  const { t } = useI18n()
  const { data, isLoading } = useQuery({
    queryKey: ["portal", "downloads"],
    queryFn: () => portal.listPortalDownloads(),
  })

  const downloads = data?.downloads || []

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("portal.downloadsTitle")}</h1>
        <p className="text-muted-foreground">{t("portal.downloadsDesc")}</p>
      </div>

      <Card>
        <CardContent className="pt-6">
          {isLoading ? (
            <div className="h-32 animate-pulse bg-muted rounded-lg" />
          ) : downloads.length === 0 ? (
            // No filters on this page, so an empty list is a genuinely
            // empty shelf: say what will appear here and where to get
            // the first one.
            <ListEmptyState
              icon={Download}
              title={t("portal.downloadsEmpty")}
              description={t("empty.downloads.desc")}
              action={{ label: t("empty.browseMarketplace"), to: "/marketplace" }}
            />
          ) : (
            <DataTable>
              <DataTableHeader>
                <DataTableRow>
                  <DataTableHead>{t("portal.downloadsProduct")}</DataTableHead>
                  <DataTableHead>{t("portal.downloadsVersion")}</DataTableHead>
                  <DataTableHead>{t("portal.downloadsChannel")}</DataTableHead>
                  <DataTableHead>{t("portal.downloadsPlatform")}</DataTableHead>
                  <DataTableHead>{t("portal.downloadsFile")}</DataTableHead>
                  <DataTableHead>{t("portal.downloadsPublished")}</DataTableHead>
                  <DataTableHead className="w-32 text-right">{t("common.actions")}</DataTableHead>
                </DataTableRow>
              </DataTableHeader>
              <DataTableBody>
                {downloads.map((d: PortalDownload) => (
                  <DataTableRow key={`${d.license_id}-${d.product_id}-${d.version}-${d.platform}-${d.filename}`}>
                    <DataTableCell className="font-medium">{d.product_name}</DataTableCell>
                    <DataTableCell>{d.version}</DataTableCell>
                    <DataTableCell>
                      <Badge variant="outline" className="capitalize">
                        {d.channel}
                      </Badge>
                    </DataTableCell>
                    <DataTableCell>{d.platform}</DataTableCell>
                    <DataTableCell>
                      <div className="flex flex-col">
                        <span className="truncate">{d.filename}</span>
                        <span className="text-xs text-muted-foreground">
                          {formatBytes(d.file_size)}
                          {d.sha256 ? ` · ${t("portal.downloadsChecksum")}: ${shortSha(d.sha256)}` : ""}
                        </span>
                      </div>
                    </DataTableCell>
                    <DataTableCell className="text-muted-foreground">{formatDate(d.published_at)}</DataTableCell>
                    <DataTableCell>
                      <div className="flex justify-end">
                        {d.download_url ? (
                          <Button size="sm" variant="outline" asChild>
                            <a href={d.download_url} target="_blank" rel="noopener noreferrer">
                              <Download className="h-4 w-4 mr-1" /> {t("portal.downloadsGet")}
                            </a>
                          </Button>
                        ) : (
                          <span className="text-xs text-muted-foreground">{t("portal.downloadsNoLink")}</span>
                        )}
                      </div>
                    </DataTableCell>
                  </DataTableRow>
                ))}
              </DataTableBody>
            </DataTable>
          )}
        </CardContent>
      </Card>
    </div>
  )
}

// formatBytes renders a file size as a plain number plus a standard
// unit abbreviation (KB/MB/…), the same way the money helpers embed the
// currency code. Nothing here is translatable UI chrome.
function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"]
  const exp = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / 1024 ** exp
  // Two decimals for anything above bytes keeps the column readable.
  return `${exp === 0 ? value : value.toFixed(2)} ${units[exp]}`
}

// A checksum is long; the table shows enough to compare, the rest is
// still on the row's tooltip-free text. The full value lives in title.
function shortSha(sha: string): string {
  return sha.length > 16 ? `${sha.slice(0, 8)}…${sha.slice(-4)}` : sha
}
