import { ArrowDown, ArrowUp, ArrowUpDown } from "lucide-react"
import type { ServerSort, SortOrder } from "@/components/ui/data-table"
import { useI18n } from "@/i18n"
import { cn } from "@/lib/utils"

// SortBar asks the server to reorder a list that renders as cards
// rather than a table — the notification feed and an order's invoice
// ledger, for two. Where a DataTableSortHead button lives inside a
// <th> and reads its state off aria-sort, these sit in a toolbar
// above the rows and use aria-pressed; the arrows are the same
// vocabulary, so the two controls read as one family. Toggle
// semantics are identical to DataTableSortHead: clicking the active
// column flips asc/desc, a different column starts in its own natural
// direction.
export function SortBar({
  sort,
  columns,
}: {
  sort: ServerSort
  columns: { column: string; label: string; firstOrder?: SortOrder }[]
}) {
  const { t } = useI18n()
  return (
    // biome-ignore lint/a11y/useSemanticElements: a fieldset's legend has its own block layout that breaks this inline toolbar; the visible label plus aria-pressed buttons carry the meaning
    <div role="group" aria-label={t("common.sortBy")} className="flex flex-wrap items-center gap-1.5">
      <span className="text-xs font-semibold uppercase tracking-wider text-muted-foreground">{t("common.sortBy")}</span>
      {columns.map(({ column, label, firstOrder }) => {
        const active = sort.sort === column
        return (
          <button
            key={column}
            type="button"
            aria-pressed={active}
            onClick={() => sort.toggle(column, firstOrder)}
            className={cn(
              "flex h-7 items-center gap-1 rounded-md border px-2 text-xs font-semibold uppercase tracking-wider transition-colors",
              active
                ? "border-foreground/30 bg-muted text-foreground"
                : "border-transparent text-muted-foreground hover:bg-muted/60 hover:text-foreground",
            )}
          >
            {label}
            {active ? (
              sort.order === "asc" ? (
                <ArrowUp className="h-3 w-3 shrink-0" />
              ) : (
                <ArrowDown className="h-3 w-3 shrink-0" />
              )
            ) : (
              <ArrowUpDown className="h-3 w-3 shrink-0 opacity-40" />
            )}
          </button>
        )
      })}
    </div>
  )
}
