import { type ClassValue, clsx } from "clsx"
import { twMerge } from "tailwind-merge"

export function cn(...inputs: ClassValue[]) {
  return twMerge(clsx(inputs))
}

// dateLocale is the locale dates are written in: the browser's language
// and region preference (en-US → 09/30/2026, en-GB → 30/09/2026,
// zh-CN → 2026/09/30), not the dashboard's UI language — an English
// dashboard does not mean the reader writes dates the American way.
export function dateLocale(): string | undefined {
  return navigator.languages?.[0] || navigator.language || undefined
}

export function formatDate(
  date: string | Date | null | undefined,
  options?: { locale?: string; timezone?: string },
): string {
  if (!date) return "-"
  const locale = options?.locale || dateLocale()
  const tz = options?.timezone || Intl.DateTimeFormat().resolvedOptions().timeZone
  return new Date(date).toLocaleDateString(locale, {
    year: "numeric",
    month: "2-digit",
    day: "2-digit",
    hour: "2-digit",
    minute: "2-digit",
    timeZone: tz,
  })
}

// formatRelativeTime renders "5 minutes ago"-style stamps through
// Intl.RelativeTimeFormat, so the wording follows the reader's
// language without a key per time unit (the notification center uses
// it for its timestamps). Anything older than a week falls back to
// the absolute date: "43 days ago" is harder to place than the date
// itself.
export function formatRelativeTime(date: string | Date | null | undefined, locale?: string): string {
  if (!date) return "-"
  const then = new Date(date).getTime()
  if (Number.isNaN(then)) return "-"
  const diffMs = then - Date.now()
  const abs = Math.abs(diffMs)
  const rtf = new Intl.RelativeTimeFormat(locale || dateLocale(), { numeric: "auto" })
  const MINUTE = 60_000
  const HOUR = 3_600_000
  const DAY = 86_400_000
  if (abs < MINUTE) return rtf.format(Math.round(diffMs / 1000), "second")
  if (abs < HOUR) return rtf.format(Math.round(diffMs / MINUTE), "minute")
  if (abs < DAY) return rtf.format(Math.round(diffMs / HOUR), "hour")
  if (abs < 7 * DAY) return rtf.format(Math.round(diffMs / DAY), "day")
  return formatDate(date)
}

export function boolColor(active: boolean): string {
  return active ? "bg-emerald-100 text-emerald-800" : "bg-gray-100 text-gray-800"
}

export function statusColor(status: string): string {
  switch (status) {
    case "active":
      return "bg-emerald-100 text-emerald-800"
    case "trialing":
      return "bg-blue-100 text-blue-800"
    case "past_due":
      return "bg-amber-100 text-amber-800"
    case "canceled":
      return "bg-gray-100 text-gray-800"
    case "expired":
      return "bg-red-100 text-red-700"
    case "suspended":
      return "bg-orange-100 text-orange-800"
    case "revoked":
      return "bg-red-200 text-red-900"
    default:
      return "bg-gray-100 text-gray-800"
  }
}
