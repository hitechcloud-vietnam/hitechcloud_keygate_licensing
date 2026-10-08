import type { LucideIcon } from "lucide-react"
import { Link } from "react-router-dom"
import { Button } from "@/components/ui/button"

// EmptyState (plan §89) is the one shape every empty table and list
// renders: WHAT the resource is (title), WHY it is empty and what it
// will hold (description), and the NEXT action (button) where one
// exists. A filtered-to-nothing list is a different story than a
// never-used feature, so callers pick the copy — this component only
// gives it the same look everywhere.
export interface EmptyStateAction {
  label: string
  onClick?: () => void
  // In-app destination; rendered as a router Link so the SPA keeps its
  // state instead of doing a full page load.
  to?: string
}

export interface EmptyStateProps {
  icon?: LucideIcon
  title: string
  description?: string
  action?: EmptyStateAction
}

export function EmptyState({ icon: Icon, title, description, action }: EmptyStateProps) {
  return (
    <div className="flex flex-col items-center gap-3 px-4 py-12 text-center">
      {Icon && (
        <div className="flex h-12 w-12 items-center justify-center rounded-full bg-muted">
          <Icon className="h-6 w-6 text-muted-foreground" aria-hidden="true" />
        </div>
      )}
      <div className="max-w-md space-y-1.5">
        <h3 className="text-sm font-semibold">{title}</h3>
        {description && <p className="text-sm text-muted-foreground">{description}</p>}
      </div>
      {action &&
        (action.to ? (
          <Button size="sm" asChild>
            <Link to={action.to}>{action.label}</Link>
          </Button>
        ) : (
          <Button size="sm" onClick={action.onClick}>
            {action.label}
          </Button>
        ))}
    </div>
  )
}

// The filter-aware half of the pattern: a list that came back empty
// because of its filters says so and offers the one obvious undo —
// clear the filters — instead of inviting the user to create the thing
// they already have and cannot see. Lists without filters omit the
// filter props entirely.
export function ListEmptyState({
  filtered = false,
  filteredTitle,
  filteredDescription,
  clearLabel,
  onClearFilters,
  ...rest
}: EmptyStateProps & {
  filtered?: boolean
  filteredTitle?: string
  filteredDescription?: string
  clearLabel?: string
  onClearFilters?: () => void
}) {
  if (!filtered) return <EmptyState {...rest} />
  return (
    <EmptyState
      {...rest}
      title={filteredTitle ?? rest.title}
      description={filteredDescription ?? rest.description}
      action={clearLabel && onClearFilters ? { label: clearLabel, onClick: onClearFilters } : rest.action}
    />
  )
}
