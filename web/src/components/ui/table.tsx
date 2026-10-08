import * as React from "react"
import { useI18n } from "@/i18n"
import { cn } from "@/lib/utils"

// The wrapper is the part that scrolls when the table is wider than
// its column. It takes focus (tabIndex) so keyboard users can scroll
// it, and names itself a region so screen-reader users know what the
// focusable box is. `label` names the contents; without it the
// generic name is used.
const Table = React.forwardRef<HTMLTableElement, React.HTMLAttributes<HTMLTableElement> & { label?: string }>(
  ({ className, label, ...props }, ref) => {
    const { t } = useI18n()
    return (
      <section
        aria-label={label ?? t("common.scrollableTable")}
        // biome-ignore lint/a11y/noNoninteractiveTabindex: scrollable region must be keyboard-scrollable (WCAG 2.1.1)
        tabIndex={0}
        className="relative w-full overflow-auto rounded-sm focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring"
      >
        <table ref={ref} className={cn("w-full caption-bottom text-sm", className)} {...props} />
      </section>
    )
  },
)
Table.displayName = "Table"

const TableHeader = React.forwardRef<HTMLTableSectionElement, React.HTMLAttributes<HTMLTableSectionElement>>(
  ({ className, ...props }, ref) => <thead ref={ref} className={cn("[&_tr]:border-b", className)} {...props} />,
)
TableHeader.displayName = "TableHeader"

const TableBody = React.forwardRef<HTMLTableSectionElement, React.HTMLAttributes<HTMLTableSectionElement>>(
  ({ className, ...props }, ref) => (
    <tbody ref={ref} className={cn("[&_tr:last-child]:border-0", className)} {...props} />
  ),
)
TableBody.displayName = "TableBody"

const TableRow = React.forwardRef<HTMLTableRowElement, React.HTMLAttributes<HTMLTableRowElement>>(
  ({ className, ...props }, ref) => (
    <tr
      ref={ref}
      className={cn("border-b transition-colors hover:bg-muted/50 data-[state=selected]:bg-muted", className)}
      {...props}
    />
  ),
)
TableRow.displayName = "TableRow"

const TableHead = React.forwardRef<HTMLTableCellElement, React.ThHTMLAttributes<HTMLTableCellElement>>(
  ({ className, ...props }, ref) => (
    <th
      ref={ref}
      scope="col"
      className={cn(
        "h-10 px-4 text-left align-middle font-medium text-muted-foreground [&:has([role=checkbox])]:pr-0",
        className,
      )}
      {...props}
    />
  ),
)
TableHead.displayName = "TableHead"

const TableCell = React.forwardRef<HTMLTableCellElement, React.TdHTMLAttributes<HTMLTableCellElement>>(
  ({ className, ...props }, ref) => (
    <td ref={ref} className={cn("p-4 align-middle [&:has([role=checkbox])]:pr-0", className)} {...props} />
  ),
)
TableCell.displayName = "TableCell"

export { Table, TableBody, TableCell, TableHead, TableHeader, TableRow }
