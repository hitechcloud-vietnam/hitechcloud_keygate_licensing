import { Download } from "lucide-react"
import { Button } from "@/components/ui/button"
import { useI18n } from "@/i18n"

// Client-side CSV export of the rows a table is showing right now
// (§70 "export"): no backend round-trip, no new API surface — the
// button serializes whatever the page already has in hand. Escaping
// follows RFC 4180 (quotes doubled, quoted when the cell holds a
// comma, quote or newline), the BOM up front so Excel reads UTF-8,
// and CRLF line endings for the same reason.

function csvCell(value: string | number | null | undefined): string {
  const s = value === null || value === undefined ? "" : String(value)
  return /[",\r\n]/.test(s) ? `"${s.replace(/"/g, '""')}"` : s
}

export function downloadCsv(
  filename: string,
  columns: (string | number)[],
  rows: (string | number | null | undefined)[][],
): void {
  const csv = [columns, ...rows].map((row) => row.map(csvCell).join(",")).join("\r\n")
  const blob = new Blob([`\uFEFF${csv}`], { type: "text/csv;charset=utf-8" })
  const url = URL.createObjectURL(blob)
  const a = document.createElement("a")
  a.href = url
  a.download = filename.endsWith(".csv") ? filename : `${filename}.csv`
  document.body.appendChild(a)
  a.click()
  a.remove()
  URL.revokeObjectURL(url)
}

// ExportCsvButton — the toolbar half of the pattern. Columns are the
// translated table headers; each row is its cells in the same order.
// Disabled with nothing to export rather than downloading a header
// line and calling it a file.
export function ExportCsvButton({
  filename,
  columns,
  rows,
  className,
}: {
  filename: string
  columns: (string | number)[]
  rows: (string | number | null | undefined)[][]
  className?: string
}) {
  const { t } = useI18n()
  return (
    <Button
      variant="outline"
      size="sm"
      className={className}
      disabled={rows.length === 0}
      onClick={() => downloadCsv(filename, columns, rows)}
    >
      <Download className="h-4 w-4 mr-2" /> {t("common.exportCsv")}
    </Button>
  )
}
