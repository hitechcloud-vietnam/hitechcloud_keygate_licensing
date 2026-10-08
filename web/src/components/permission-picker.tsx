// PermissionPicker (plan §8 RBAC): the §8 vocabulary grouped into
// family fieldsets with a per-family "all" toggle. The vocabulary
// itself comes from GET /admin/rbac/permissions — never hardcoded —
// and each grant renders through its rbac.permissions.* i18n key.
// The same picker serves the create dialog and the role detail page,
// so a role is edited with one vocabulary in one shape everywhere.
import { type TranslationKeys, useI18n } from "@/i18n"
import { cn } from "@/lib/utils"

// The families of the §8 vocabulary, in plan order. A permission
// outside these families (a server-side addition) still renders — in
// its own family derived from its prefix.
export const PERMISSION_FAMILIES = [
  "products",
  "plans",
  "licenses",
  "orders",
  "payments",
  "customers",
  "reports",
  "audit",
  "settings",
] as const

export type PermissionFamily = (typeof PERMISSION_FAMILIES)[number]

export function permissionFamily(permission: string): string {
  const family = permission.split(".")[0]
  return (PERMISSION_FAMILIES as readonly string[]).includes(family) ? family : "other"
}

export function permissionLabel(
  t: (key: TranslationKeys, params?: Record<string, string | number>) => string,
  permission: string,
): string {
  const key = `rbac.permissions.${permission}` as TranslationKeys
  const text = t(key)
  // A grant the catalog does not carry in the i18n files degrades to
  // its readable form ("licenses.activate" → "Licenses activate")
  // rather than showing a raw key.
  return text === key ? permission.replace(/[._]/g, " ") : text
}

interface PermissionPickerProps {
  /** The vocabulary (usually GET /admin/rbac/permissions). */
  permissions: string[]
  /** The currently selected grants. */
  value: string[]
  onChange: (next: string[]) => void
  /** Read-only mode — used for built-in (system) roles. */
  disabled?: boolean
}

export function PermissionPicker({ permissions, value, onChange, disabled }: PermissionPickerProps) {
  const { t } = useI18n()
  const selected = new Set(value)

  // Group by family, keeping the vocabulary's own order inside each.
  const families: { family: string; perms: string[] }[] = []
  for (const p of permissions) {
    const family = permissionFamily(p)
    const bucket = families.find((f) => f.family === family)
    if (bucket) bucket.perms.push(p)
    else families.push({ family, perms: [p] })
  }

  const toggle = (permission: string, on: boolean) => {
    const next = new Set(selected)
    if (on) next.add(permission)
    else next.delete(permission)
    onChange(permissions.filter((p) => next.has(p)))
  }

  // "All in family" flips the whole fieldset in one move — ticking ten
  // boxes one by one is how grants get missed.
  const toggleFamily = (perms: string[], on: boolean) => {
    const next = new Set(selected)
    for (const p of perms) {
      if (on) next.add(p)
      else next.delete(p)
    }
    onChange(permissions.filter((p) => next.has(p)))
  }

  return (
    <div className="space-y-4">
      {families.map(({ family, perms }) => {
        const allOn = perms.every((p) => selected.has(p))
        const familyLabel = t(`rbac.families.${family}` as TranslationKeys)
        return (
          <fieldset key={family} className="rounded-md border px-3 pb-3 pt-2">
            <legend className="px-1 text-sm font-semibold">{familyLabel}</legend>
            <label className="flex items-center gap-2 border-b pb-2 text-xs font-medium text-muted-foreground">
              <input
                type="checkbox"
                checked={allOn}
                disabled={disabled}
                onChange={(e) => toggleFamily(perms, e.target.checked)}
                className="h-4 w-4 rounded border-input accent-primary"
              />
              {t("rbac.familyAll")}
            </label>
            <div className="mt-2 grid gap-2 sm:grid-cols-2">
              {perms.map((p) => (
                <label key={p} className={cn("flex items-center gap-2 text-sm", disabled && "opacity-60")}>
                  <input
                    type="checkbox"
                    checked={selected.has(p)}
                    disabled={disabled}
                    onChange={(e) => toggle(p, e.target.checked)}
                    className="h-4 w-4 rounded border-input accent-primary"
                  />
                  <span className="min-w-0 truncate">{permissionLabel(t, p)}</span>
                  <code className="ml-auto hidden shrink-0 text-xs text-muted-foreground lg:block">{p}</code>
                </label>
              ))}
            </div>
          </fieldset>
        )
      })}
    </div>
  )
}
