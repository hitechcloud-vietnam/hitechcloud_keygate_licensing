import { Package } from "lucide-react"
import { Link } from "react-router-dom"
import { StarRating } from "@/components/star-rating"
import { Badge } from "@/components/ui/badge"
import { Card, CardContent } from "@/components/ui/card"
import { useI18n } from "@/i18n"
import type { MarketplaceProduct } from "@/lib/api"
import { formatMinor } from "@/lib/money"

// ProductCard — one catalog entry: identity (logo/name/handle), the
// star aggregate, a short blurb, category chips, and the plan summary.
// The "from" price appears only when a plan actually carries one (rare:
// prices live on Stripe and the marketplace payload nulls them today).
//
// Shared by the marketplace listing and the product page's related-
// products rail, so both render exactly one card shape. The rating
// badge is the approved-reviews aggregate the payload carries
// (rating_average_bps / rating_count) — hidden when there are none,
// rather than drawing an empty five-star row on every unreviewed card.
export function ProductCard({ product }: { product: MarketplaceProduct }) {
  const { t } = useI18n()
  const priced = product.plans.filter((p) => p.price != null && p.currency)
  const from = priced.length ? priced.reduce((a, b) => ((a.price ?? 0) <= (b.price ?? 0) ? a : b)) : null
  return (
    <Link to={`/marketplace/products/${product.slug}`} className="group block">
      <Card className="h-full transition-shadow group-hover:shadow-md">
        <CardContent className="space-y-3 p-5">
          <div className="flex items-start gap-3">
            {product.logo_url ? (
              <img src={product.logo_url} alt="" className="h-10 w-10 rounded object-contain" />
            ) : (
              <div className="flex h-10 w-10 items-center justify-center rounded bg-muted">
                <Package className="h-5 w-5 text-muted-foreground" />
              </div>
            )}
            <div className="min-w-0">
              <h3 className="truncate font-semibold">{product.name}</h3>
              <code className="text-xs text-muted-foreground">{product.slug}</code>
            </div>
          </div>

          {(product.rating_count ?? 0) > 0 && (
            <StarRating valueBps={product.rating_average_bps ?? 0} count={product.rating_count} showValue size="sm" />
          )}

          {product.short_description && (
            <p className="line-clamp-2 text-sm text-muted-foreground">{product.short_description}</p>
          )}

          {product.categories.length > 0 && (
            <div className="flex flex-wrap gap-1">
              {product.categories.map((c) => (
                <Badge key={c.id} variant="outline">
                  {c.name}
                </Badge>
              ))}
            </div>
          )}

          <div className="flex items-center justify-between text-sm">
            <span className="text-muted-foreground">
              {product.plans.length === 1
                ? t("marketplace.plansCountOne")
                : t("marketplace.plansCount", { count: product.plans.length })}
            </span>
            {from?.price != null && from.currency && (
              <span className="font-medium">
                {t("marketplace.fromPrice", { price: formatMinor(from.price, from.currency) })}
              </span>
            )}
          </div>
        </CardContent>
      </Card>
    </Link>
  )
}
