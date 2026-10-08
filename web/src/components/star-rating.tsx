import { Star } from "lucide-react"
import { useId } from "react"
import { useI18n } from "@/i18n"
import { cn } from "@/lib/utils"

// ─── Star ratings (marketplace reviews) ───
//
// The aggregate the marketplace serves is integer basis points
// (model.RatingAggregate: 10000 = 1 star … 50000 = 5 stars), the same
// no-float discipline as money and rates. These components own the one
// place that turns bps into stars, so every surface — catalog cards,
// the product page, moderation — agrees on what "4.5 stars" looks like.
//
// Display is read-only and fills each star fully, by halves, or not at
// all (remainder ≥ 5000 bps rounds up to a half star). The input
// variant is whole stars only (1..5): a review rating is an integer
// (model.ValidReviewRating), so a half here would be a rating the API
// refuses.

const BPS_PER_STAR = 10000

// starsFromBps converts an average in basis points to stars in 0..5.
export function starsFromBps(bps: number): number {
  return Math.min(5, Math.max(0, bps) / BPS_PER_STAR)
}

// formatStars renders the numeric average beside a star row as "4.5".
// One decimal, computed from the integer by integer steps (tenths =
// round(bps/1000)), so 43500 bps reads "4.4" on every browser instead
// of drifting through a float the way (4.35).toFixed(1) can.
export function formatStars(bps: number): string {
  const tenths = Math.round(Math.min(50000, Math.max(0, bps)) / 1000)
  return `${Math.floor(tenths / 10)}.${tenths % 10}`
}

// StarGlyph is one star, filled to fillPct (0, 50 or 100) by overlaying
// a clipped solid star on an empty outline.
function StarGlyph({ fillPct, iconCls }: { fillPct: number; iconCls: string }) {
  return (
    <span className="relative inline-block">
      <Star className={cn(iconCls, "text-amber-500/30")} strokeWidth={1.5} />
      {fillPct > 0 && (
        <span className="absolute inset-0 overflow-hidden" style={{ width: `${fillPct}%` }}>
          <Star className={cn(iconCls, "text-amber-500")} fill="currentColor" strokeWidth={0} />
        </span>
      )}
    </span>
  )
}

// StarRating — read-only star display driven by integer basis points.
// valueBps is the aggregate's average_bps (or rating × 10000 for one
// review's whole stars). showValue adds the "4.5" beside the row,
// count adds the "(12)" it was drawn from.
export function StarRating({
  valueBps,
  showValue = false,
  count,
  size = "md",
  className,
}: {
  valueBps: number
  showValue?: boolean
  count?: number
  size?: "sm" | "md"
  className?: string
}) {
  const { t } = useI18n()
  const clamped = Math.min(50000, Math.max(0, Math.round(valueBps)))
  const full = Math.floor(clamped / BPS_PER_STAR)
  const half = clamped % BPS_PER_STAR >= BPS_PER_STAR / 2
  const iconCls = size === "sm" ? "h-3.5 w-3.5" : "h-4 w-4"
  return (
    <span className={cn("inline-flex items-center gap-1", className)}>
      <span
        role="img"
        aria-label={t("reviews.starsLabel", { value: formatStars(clamped) })}
        className="inline-flex items-center"
      >
        {[1, 2, 3, 4, 5].map((n) => (
          <StarGlyph key={n} fillPct={n <= full ? 100 : n === full + 1 && half ? 50 : 0} iconCls={iconCls} />
        ))}
      </span>
      {showValue && <span className="text-sm font-medium">{formatStars(clamped)}</span>}
      {count !== undefined && <span className="text-xs text-muted-foreground">({count})</span>}
    </span>
  )
}

// StarRatingInput — interactive 1..5 whole-star picker (a review
// rating is an integer). Native radios under the stars: the fieldset
// names the group, each star is its radio's implicit label, and
// keyboard choice follows the browser's own radio-group behaviour.
export function StarRatingInput({
  value,
  onChange,
  disabled = false,
  size = "md",
}: {
  value: number
  onChange: (rating: number) => void
  disabled?: boolean
  size?: "sm" | "md"
}) {
  const { t } = useI18n()
  const name = useId()
  const iconCls = size === "sm" ? "h-5 w-5" : "h-6 w-6"
  return (
    <fieldset className="inline-flex items-center gap-1" disabled={disabled}>
      <legend className="sr-only">{t("reviews.ratingLabel")}</legend>
      {[1, 2, 3, 4, 5].map((n) => (
        <label
          key={n}
          className={cn(
            "rounded transition-transform hover:scale-110",
            disabled ? "cursor-not-allowed opacity-50" : "cursor-pointer",
          )}
        >
          <input
            type="radio"
            name={name}
            value={n}
            checked={value === n}
            onChange={() => onChange(n)}
            className="sr-only"
          />
          <span className="sr-only">{t("reviews.rateStars", { count: n })}</span>
          <Star
            aria-hidden="true"
            className={cn(iconCls, n <= value ? "text-amber-500" : "text-amber-500/30")}
            fill={n <= value ? "currentColor" : "none"}
            strokeWidth={n <= value ? 0 : 1.5}
          />
        </label>
      ))}
    </fieldset>
  )
}
