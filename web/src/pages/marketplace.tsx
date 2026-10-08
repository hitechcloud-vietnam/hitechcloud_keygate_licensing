import { useQuery } from "@tanstack/react-query"
import { AlertCircle, ChevronLeft, ChevronRight, Package, Search } from "lucide-react"
import { useState } from "react"
import { Link } from "react-router-dom"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent } from "@/components/ui/card"
import { useServerPagination } from "@/components/ui/data-table"
import { Input } from "@/components/ui/input"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { useI18n } from "@/i18n"
import type { MarketplaceProduct } from "@/lib/api"
import { marketplace } from "@/lib/api"
import { formatMinor } from "@/lib/money"
import { useDebounced } from "@/lib/use-debounced"

// The public storefront's discovery listing (plan §30 + Phase 6):
// search + category filter + sort over the anonymous
// GET /marketplace/products catalog. Rendered inside PublicLayout, so
// the header/footer/attribution live there. The API accepts only
// sort=name|newest and answers data.{products,total,limit,offset};
// prices are null here (they live on Stripe), so a card shows its plan
// count and a "from" price only when one is present.
export default function MarketplacePage() {
  const { t } = useI18n()
  const [searchInput, setSearchInput] = useState("")
  const search = useDebounced(searchInput, 300)
  const [category, setCategory] = useState("") // "" = all categories
  const [sort, setSort] = useState<"newest" | "name">("newest")
  const pg = useServerPagination(12, [search, category, sort])

  const categoriesQuery = useQuery({
    queryKey: ["marketplace", "categories"],
    queryFn: () => marketplace.categories(),
    staleTime: 60_000,
  })

  // The API's sort vocabulary is just name|newest, each with its
  // natural direction. Anything else is a 400, so we only ever send
  // these two.
  const sortParams =
    sort === "name" ? { sort: "name", order: "asc" as const } : { sort: "newest", order: "desc" as const }

  const productsQuery = useQuery({
    queryKey: ["marketplace", "products", search, category, sort, pg.page, pg.pageSize],
    queryFn: () =>
      marketplace.products({
        search: search || undefined,
        category: category || undefined,
        ...sortParams,
        ...pg.params,
      }),
  })

  const data = productsQuery.data
  const { items: products, total, totalPages } = pg.from(data, data?.products)
  const offset = data?.offset ?? 0
  const from = products.length ? offset + 1 : 0
  const to = offset + products.length

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-3xl font-bold tracking-tight">{t("marketplace.title")}</h1>
        <p className="text-muted-foreground">{t("marketplace.subtitle")}</p>
      </div>

      {/* Filters */}
      <div className="flex flex-wrap items-center gap-3">
        <div className="relative w-full sm:w-72">
          <Search className="absolute left-3 top-1/2 -translate-y-1/2 h-4 w-4 text-muted-foreground" />
          <Input
            placeholder={t("marketplace.searchPlaceholder")}
            value={searchInput}
            onChange={(e) => {
              setSearchInput(e.target.value)
              pg.setPage(0)
            }}
            className="pl-9"
          />
        </div>

        <Select
          value={category || "all"}
          onValueChange={(v) => {
            setCategory(v === "all" ? "" : v)
            pg.setPage(0)
          }}
        >
          <SelectTrigger className="w-full sm:w-52" aria-label={t("marketplace.categoryLabel")}>
            <SelectValue placeholder={t("marketplace.categoryLabel")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="all">{t("marketplace.allCategories")}</SelectItem>
            {(categoriesQuery.data?.categories || []).map((c) => (
              <SelectItem key={c.id} value={c.slug}>
                {c.name}
              </SelectItem>
            ))}
          </SelectContent>
        </Select>

        <Select
          value={sort}
          onValueChange={(v) => {
            setSort(v === "name" ? "name" : "newest")
            pg.setPage(0)
          }}
        >
          <SelectTrigger className="w-full sm:w-44" aria-label={t("marketplace.sortLabel")}>
            <SelectValue placeholder={t("marketplace.sortLabel")} />
          </SelectTrigger>
          <SelectContent>
            <SelectItem value="newest">{t("marketplace.sortNewest")}</SelectItem>
            <SelectItem value="name">{t("marketplace.sortName")}</SelectItem>
          </SelectContent>
        </Select>
      </div>

      {/* Results */}
      {productsQuery.isLoading ? (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {["s1", "s2", "s3", "s4", "s5", "s6"].map((k) => (
            <div key={k} className="h-48 animate-pulse bg-muted rounded-lg" />
          ))}
        </div>
      ) : productsQuery.isError ? (
        <Card>
          <CardContent className="flex flex-col items-center gap-3 py-10 text-center">
            <div className="flex items-center gap-2 text-destructive">
              <AlertCircle className="h-5 w-5" />
              <span>{t("marketplace.loadError")}</span>
            </div>
            <Button variant="outline" onClick={() => productsQuery.refetch()}>
              {t("common.retry")}
            </Button>
          </CardContent>
        </Card>
      ) : products.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center text-muted-foreground">{t("marketplace.empty")}</CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-3">
          {products.map((p) => (
            <ProductCard key={p.id} product={p} />
          ))}
        </div>
      )}

      {/* Pager */}
      {total > 0 && (
        <div className="flex items-center justify-between">
          <p className="text-sm text-muted-foreground">{t("marketplace.showingRange", { from, to, total })}</p>
          <div className="flex gap-2">
            <Button variant="outline" size="sm" disabled={pg.page === 0} onClick={() => pg.setPage(pg.page - 1)}>
              <ChevronLeft className="h-4 w-4 mr-1" /> {t("marketplace.prev")}
            </Button>
            <Button
              variant="outline"
              size="sm"
              disabled={pg.page >= totalPages - 1}
              onClick={() => pg.setPage(pg.page + 1)}
            >
              {t("marketplace.next")} <ChevronRight className="h-4 w-4 ml-1" />
            </Button>
          </div>
        </div>
      )}
    </div>
  )
}

// ProductCard — one catalog entry: identity (logo/name/handle), a
// short blurb, category chips, and the plan summary. The "from" price
// appears only when a plan actually carries one (rare: prices live on
// Stripe and the marketplace payload nulls them today).
function ProductCard({ product }: { product: MarketplaceProduct }) {
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
