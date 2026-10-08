import { useMutation, useQuery, useQueryClient } from "@tanstack/react-query"
import { AlertCircle, ArrowLeft, BookOpen, Download, Github, Globe, Package, ShoppingCart } from "lucide-react"
import { useState } from "react"
import { Link, useLocation, useParams } from "react-router-dom"
import { ProductCard } from "@/components/product-card"
import { StarRating, StarRatingInput } from "@/components/star-rating"
import { showToast, toastError } from "@/components/toast"
import {
  AlertDialog,
  AlertDialogAction,
  AlertDialogCancel,
  AlertDialogContent,
  AlertDialogDescription,
  AlertDialogHeader,
  AlertDialogTitle,
} from "@/components/ui/alert-dialog"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { useServerPagination } from "@/components/ui/data-table"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Separator } from "@/components/ui/separator"
import { useAuth } from "@/hooks/use-auth"
import { useI18n } from "@/i18n"
import type { MarketplacePlan, MarketplaceProduct, PublicReview } from "@/lib/api"
import { ApiError, marketplace, portal } from "@/lib/api"
import { attributionFromSearch, withAttribution } from "@/lib/attribution"
import { useCart } from "@/lib/cart"
import { formatMinor } from "@/lib/money"
import { formatDate } from "@/lib/utils"

// The public product page (plan §30 + Phase 6): GET
// /marketplace/products/:slug → data.product. An unknown slug answers
// 404 exactly like a failed lookup, so we show one friendly not-found
// state. The §223 catalog enrichment fields (description, logo, images,
// doc/website/repo URLs, vendor) are rendered when present and
// tolerated when null/absent — the API may not carry them yet.
// Below the catalog: the Reviews section (approved rows + aggregate +
// the signed-in customer's write form) and the related-products rail.
export default function MarketplaceProductPage() {
  const { t } = useI18n()
  const { slug = "" } = useParams()

  const query = useQuery({
    queryKey: ["marketplace", "product", slug],
    enabled: !!slug,
    queryFn: () => marketplace.product(slug),
    // A 404 is a definite answer, not a hiccup — don't retry it.
    retry: (count, error) => !(error instanceof ApiError && error.status === 404) && count < 1,
  })

  if (query.isLoading) {
    return (
      <div className="space-y-6">
        <div className="h-40 animate-pulse bg-muted rounded-lg" />
        <div className="h-64 animate-pulse bg-muted rounded-lg" />
      </div>
    )
  }

  // Friendly not-found for an unknown slug (or one that failed lookup).
  const notFound = query.error instanceof ApiError && query.error.status === 404
  if (query.isError || !query.data) {
    return (
      <Card>
        <CardContent className="flex flex-col items-center gap-4 py-14 text-center">
          {notFound ? (
            <>
              <Package className="h-10 w-10 text-muted-foreground" />
              <div>
                <h2 className="text-lg font-semibold">{t("marketplace.notFoundTitle")}</h2>
                <p className="text-muted-foreground">{t("marketplace.notFoundBody")}</p>
              </div>
              <Button asChild variant="outline">
                <Link to="/marketplace">
                  <ArrowLeft className="h-4 w-4 mr-2" /> {t("marketplace.backToMarketplace")}
                </Link>
              </Button>
            </>
          ) : (
            <>
              <div className="flex items-center gap-2 text-destructive">
                <AlertCircle className="h-5 w-5" />
                <span>{t("marketplace.loadError")}</span>
              </div>
              <Button variant="outline" onClick={() => query.refetch()}>
                {t("common.retry")}
              </Button>
            </>
          )}
        </CardContent>
      </Card>
    )
  }

  const product = query.data.product
  return <ProductDetail product={product} />
}

function ProductDetail({ product }: { product: MarketplaceProduct }) {
  const { t } = useI18n()
  // The attribution the visitor arrived with. It follows every Buy
  // button to the checkout page, which forwards it on to the server
  // /pay route — where the sale is actually attributed.
  const { search } = useLocation()
  const attribution = attributionFromSearch(search)
  // Cart (plan §23): "add to plan" fills the localStorage cart the
  // badge in the header counts; the server re-prices at checkout.
  const { add: addCartItem } = useCart()
  const addPlan = (p: MarketplacePlan) => {
    addCartItem({
      plan_id: p.id,
      product_name: product.name,
      plan_name: p.name,
      unit_amount_minor: p.price ?? 0,
      currency: p.currency ?? "",
    })
    showToast(t("cart.added"), "success")
  }
  const images = product.images || []
  const hasLinks = Boolean(
    product.documentation_url || product.website_url || product.repository_url || product.download_url,
  )

  return (
    <div className="space-y-8">
      <Button asChild variant="ghost" size="sm" className="-ml-2">
        <Link to="/marketplace">
          <ArrowLeft className="h-4 w-4 mr-2" /> {t("marketplace.backToMarketplace")}
        </Link>
      </Button>

      {/* Hero */}
      <div className="flex flex-wrap items-start gap-5">
        {product.logo_url ? (
          <img src={product.logo_url} alt="" className="h-20 w-20 rounded-lg object-contain" />
        ) : (
          <div className="flex h-20 w-20 items-center justify-center rounded-lg bg-muted">
            <Package className="h-9 w-9 text-muted-foreground" />
          </div>
        )}
        <div className="min-w-0 flex-1">
          <div className="flex flex-wrap items-center gap-2">
            <h1 className="text-3xl font-bold tracking-tight">{product.name}</h1>
            <Badge variant="secondary">{product.type}</Badge>
          </div>
          <code className="text-sm text-muted-foreground">{product.slug}</code>
          {product.short_description && <p className="mt-2 text-muted-foreground">{product.short_description}</p>}
          <div className="mt-3 flex flex-wrap items-center gap-2">
            {product.categories.map((c) => (
              <Badge key={c.id} variant="outline">
                {c.name}
              </Badge>
            ))}
            {product.vendor && (
              <span className="text-sm text-muted-foreground">
                {t("marketplace.vendor")}: {product.vendor}
              </span>
            )}
          </div>
        </div>
      </div>

      {/* Images */}
      {images.length > 0 && (
        <div className="grid grid-cols-2 gap-3 sm:grid-cols-3 lg:grid-cols-4">
          {images.map((src) => (
            <img key={src} src={src} alt="" className="aspect-video w-full rounded-lg border object-cover" />
          ))}
        </div>
      )}

      <div className="grid grid-cols-1 gap-8 lg:grid-cols-3">
        <div className="space-y-8 lg:col-span-2">
          {/* Overview / long description */}
          {(product.description || product.minimum_supported_version || product.minimum_supported_message) && (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">{t("marketplace.overview")}</CardTitle>
              </CardHeader>
              <CardContent className="space-y-4">
                {product.description && (
                  <p className="whitespace-pre-wrap text-sm leading-relaxed">{product.description}</p>
                )}
                {product.minimum_supported_version && (
                  <p className="text-sm text-muted-foreground">
                    {t("marketplace.minVersion", { version: product.minimum_supported_version })}
                  </p>
                )}
                {product.minimum_supported_message && (
                  <p className="text-sm text-muted-foreground">{product.minimum_supported_message}</p>
                )}
              </CardContent>
            </Card>
          )}

          {/* Plans */}
          <Card>
            <CardHeader>
              <CardTitle className="text-base">{t("marketplace.plans")}</CardTitle>
              <p className="text-sm text-muted-foreground">{t("marketplace.plansDesc")}</p>
            </CardHeader>
            <CardContent>
              {product.plans.length === 0 ? (
                <p className="py-6 text-center text-sm text-muted-foreground">{t("marketplace.noPlans")}</p>
              ) : (
                <div className="overflow-x-auto">
                  <table className="w-full text-sm">
                    <thead>
                      <tr className="border-b text-left text-xs uppercase tracking-wider text-muted-foreground">
                        <th className="py-2 pr-3">{t("marketplace.colPlan")}</th>
                        <th className="py-2 pr-3">{t("marketplace.colLicenseType")}</th>
                        <th className="py-2 pr-3">{t("marketplace.colBilling")}</th>
                        <th className="py-2 text-right">{t("common.actions")}</th>
                      </tr>
                    </thead>
                    <tbody>
                      {product.plans.map((p) => (
                        <tr key={p.id} className="border-b last:border-0">
                          <td className="py-3 pr-3">
                            <div className="font-medium">{p.name}</div>
                            <code className="text-xs text-muted-foreground">{p.slug}</code>
                          </td>
                          <td className="py-3 pr-3">
                            <Badge variant="outline">{p.license_type}</Badge>
                          </td>
                          <td className="py-3 pr-3 text-muted-foreground">
                            {p.billing_interval || "—"}
                            {p.price != null && p.currency && (
                              <div className="font-medium text-foreground">{formatMinor(p.price, p.currency)}</div>
                            )}
                          </td>
                          <td className="py-3 text-right">
                            <div className="flex items-center justify-end gap-2">
                              <Button variant="outline" size="sm" onClick={() => addPlan(p)}>
                                <ShoppingCart className="h-4 w-4 mr-1" /> {t("cart.add")}
                              </Button>
                              {p.checkout_id && (
                                <Button asChild size="sm">
                                  <Link to={withAttribution(`/checkout/${p.checkout_id}`, attribution)}>
                                    {t("marketplace.buy")}
                                  </Link>
                                </Button>
                              )}
                            </div>
                          </td>
                        </tr>
                      ))}
                    </tbody>
                  </table>
                </div>
              )}
            </CardContent>
          </Card>

          {/* Changelog / releases */}
          <Card>
            <CardHeader>
              <CardTitle className="text-base">{t("marketplace.changelog")}</CardTitle>
              <p className="text-sm text-muted-foreground">{t("marketplace.changelogDesc")}</p>
            </CardHeader>
            <CardContent className="space-y-6">
              {!product.releases || product.releases.length === 0 ? (
                <p className="py-6 text-center text-sm text-muted-foreground">{t("marketplace.noReleases")}</p>
              ) : (
                product.releases.map((r) => (
                  <div key={`${r.version}-${r.channel}`}>
                    <div className="flex flex-wrap items-center gap-2">
                      <span className="font-semibold">{r.version}</span>
                      <Badge variant="secondary">{r.channel}</Badge>
                      {r.name && <span className="text-sm text-muted-foreground">{r.name}</span>}
                      {r.published_at && (
                        <span className="ml-auto text-xs text-muted-foreground">
                          {t("marketplace.publishedOn", { date: formatDate(r.published_at) })}
                        </span>
                      )}
                    </div>
                    {r.release_notes && (
                      <p className="mt-2 whitespace-pre-wrap text-sm text-muted-foreground">{r.release_notes}</p>
                    )}
                    {r.artifacts.length > 0 && (
                      <div className="mt-3 overflow-x-auto">
                        <table className="w-full text-xs">
                          <thead>
                            <tr className="border-b text-left uppercase tracking-wider text-muted-foreground">
                              <th className="py-1.5 pr-3">{t("marketplace.colPlatform")}</th>
                              <th className="py-1.5 pr-3">{t("marketplace.colFile")}</th>
                              <th className="py-1.5 pr-3">{t("marketplace.colSize")}</th>
                              <th className="py-1.5">{t("marketplace.colChecksum")}</th>
                            </tr>
                          </thead>
                          <tbody>
                            {r.artifacts.map((a) => (
                              <tr key={`${a.platform}-${a.filename}`} className="border-b last:border-0">
                                <td className="py-1.5 pr-3">
                                  <Badge variant="outline">{a.platform}</Badge>
                                </td>
                                <td className="py-1.5 pr-3 font-mono">{a.filename}</td>
                                <td className="py-1.5 pr-3">{formatBytes(a.file_size)}</td>
                                <td className="py-1.5 font-mono text-muted-foreground" title={a.sha256}>
                                  {shortSha(a.sha256)}
                                </td>
                              </tr>
                            ))}
                          </tbody>
                        </table>
                      </div>
                    )}
                    <Separator className="mt-6" />
                  </div>
                ))
              )}
            </CardContent>
          </Card>

          {/* Reviews: aggregate + approved rows + the write side */}
          <ReviewsSection product={product} />
        </div>

        {/* Sidebar: links */}
        <div className="space-y-6">
          {hasLinks && (
            <Card>
              <CardHeader>
                <CardTitle className="text-base">{t("marketplace.links")}</CardTitle>
              </CardHeader>
              <CardContent className="space-y-2">
                {product.documentation_url && (
                  <a
                    href={product.documentation_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="flex items-center gap-2 text-sm text-primary hover:underline"
                  >
                    <BookOpen className="h-4 w-4" /> {t("marketplace.docLink")}
                  </a>
                )}
                {product.website_url && (
                  <a
                    href={product.website_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="flex items-center gap-2 text-sm text-primary hover:underline"
                  >
                    <Globe className="h-4 w-4" /> {t("marketplace.websiteLink")}
                  </a>
                )}
                {product.repository_url && (
                  <a
                    href={product.repository_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="flex items-center gap-2 text-sm text-primary hover:underline"
                  >
                    <Github className="h-4 w-4" /> {t("marketplace.repoLink")}
                  </a>
                )}
                {product.download_url && (
                  <a
                    href={product.download_url}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="flex items-center gap-2 text-sm text-primary hover:underline"
                  >
                    <Download className="h-4 w-4" /> {t("marketplace.downloadPage")}
                  </a>
                )}
              </CardContent>
            </Card>
          )}
        </div>
      </div>

      {/* Related products */}
      <RelatedRail product={product} />
    </div>
  )
}

// ─── Reviews (marketplace read side + portal write side) ───
//
// The section reads GET /marketplace/products/:slug/reviews: one page
// of APPROVED reviews plus the aggregate in the same response
// (rating_average_bps / rating_count — integer basis points, 10000 =
// 1 star). The write side is the portal's one-review-per-product seam:
// POST starts a pending review, PATCH edits the author's own words,
// DELETE takes it down. There is no read endpoint for one's own review,
// so "you already have one" is discovered the honest way — the 409
// DUPLICATE answer to a submit — and the form flips to edit mode with
// a friendly note when it arrives.
function ReviewsSection({ product }: { product: MarketplaceProduct }) {
  const { t } = useI18n()
  const pg = useServerPagination(5, [product.id])

  const reviewsQuery = useQuery({
    queryKey: ["marketplace", "product", product.slug, "reviews", pg.page, pg.pageSize],
    queryFn: () => marketplace.reviews(product.slug, pg.params),
  })

  const data = reviewsQuery.data
  const { items: reviews, total, totalPages } = pg.from(data, data?.reviews)
  const offset = data?.offset ?? 0
  const from = reviews.length ? offset + 1 : 0
  const to = offset + reviews.length
  // The list response carries the aggregate beside its rows; before it
  // lands (or if the fetch failed) the product page's own aggregate
  // stands in — the two are the same approved-reviews summary.
  const avgBps = data?.rating_average_bps ?? product.rating_average_bps ?? 0
  const count = data?.rating_count ?? product.rating_count ?? 0

  return (
    <Card>
      <CardHeader>
        <CardTitle className="text-base">{t("marketplace.reviewsSection")}</CardTitle>
      </CardHeader>
      <CardContent className="space-y-6">
        {/* Aggregate header: stars + numeric average + count */}
        {count > 0 ? (
          <div className="flex flex-wrap items-center gap-3">
            <StarRating valueBps={avgBps} showValue />
            <span className="text-sm text-muted-foreground">
              {count === 1 ? t("marketplace.reviewsCountOne") : t("marketplace.reviewsCount", { count })}
            </span>
          </div>
        ) : (
          <p className="text-sm text-muted-foreground">{t("marketplace.reviewsEmpty")}</p>
        )}

        {/* One page of approved reviews, newest first */}
        {reviewsQuery.isLoading ? (
          <div className="h-24 animate-pulse bg-muted rounded-lg" />
        ) : reviewsQuery.isError ? (
          <div className="flex items-center gap-3 py-4">
            <div className="flex items-center gap-2 text-destructive">
              <AlertCircle className="h-5 w-5" />
              <span>{t("marketplace.loadError")}</span>
            </div>
            <Button variant="outline" size="sm" onClick={() => reviewsQuery.refetch()}>
              {t("common.retry")}
            </Button>
          </div>
        ) : (
          <div className="space-y-6">
            {reviews.map((r) => (
              <ReviewItem key={r.id} review={r} />
            ))}
          </div>
        )}

        {/* Pager */}
        {total > pg.pageSize && (
          <div className="flex items-center justify-between">
            <p className="text-sm text-muted-foreground">{t("marketplace.showingRange", { from, to, total })}</p>
            <div className="flex gap-2">
              <Button variant="outline" size="sm" disabled={pg.page === 0} onClick={() => pg.setPage(pg.page - 1)}>
                {t("marketplace.prev")}
              </Button>
              <Button
                variant="outline"
                size="sm"
                disabled={pg.page >= totalPages - 1}
                onClick={() => pg.setPage(pg.page + 1)}
              >
                {t("marketplace.next")}
              </Button>
            </div>
          </div>
        )}

        <Separator />
        <ReviewForm
          product={product}
          onChanged={() => {
            reviewsQuery.refetch()
          }}
        />
      </CardContent>
    </Card>
  )
}

// ReviewItem — one approved review: stars, headline, the author's
// display name and date, the body, and the vendor's public answer
// when moderation wrote one.
function ReviewItem({ review }: { review: PublicReview }) {
  const { t } = useI18n()
  return (
    <div className="space-y-2 border-b pb-6 last:border-0 last:pb-0">
      <div className="flex flex-wrap items-center gap-x-3 gap-y-1">
        <StarRating valueBps={review.rating * 10000} size="sm" />
        {review.title && <span className="font-medium">{review.title}</span>}
        <span className="ml-auto text-xs text-muted-foreground">{formatDate(review.created_at)}</span>
      </div>
      <p className="text-sm text-muted-foreground">{review.customer_name || t("reviews.anonymous")}</p>
      <p className="whitespace-pre-wrap text-sm leading-relaxed">{review.body}</p>
      {review.admin_reply && (
        <div className="rounded-md border bg-muted/50 p-3">
          <p className="text-xs font-medium">{t("reviews.adminReplyLabel")}</p>
          <p className="mt-1 whitespace-pre-wrap text-sm text-muted-foreground">{review.admin_reply}</p>
        </div>
      )}
    </div>
  )
}

// ReviewForm — the signed-in customer's write side of one product's
// review. Starts in write mode (POST); a 409 DUPLICATE answer flips it
// to edit mode (PATCH/DELETE) with a note, because the API has no
// "your review" read endpoint: one review per (product, session
// account), and only the author can ever see or touch their own row.
function ReviewForm({ product, onChanged }: { product: MarketplaceProduct; onChanged: () => void }) {
  const { t } = useI18n()
  const qc = useQueryClient()
  const { user, loading } = useAuth()
  const [mode, setMode] = useState<"write" | "edit">("write")
  const [rating, setRating] = useState(0)
  const [title, setTitle] = useState("")
  const [body, setBody] = useState("")
  // The title the form was filled from, when we know it (a review we
  // just created). Undefined after a 409 DUPLICATE: the PATCH then
  // names the title only when one is typed, so a blank box keeps the
  // stored one instead of silently wiping it.
  const [knownTitle, setKnownTitle] = useState<string | undefined>(undefined)
  const [notice, setNotice] = useState<string | null>(null)
  const [confirmingDelete, setConfirmingDelete] = useState(false)

  const invalidate = () => {
    qc.invalidateQueries({ queryKey: ["marketplace", "product", product.slug, "reviews"] })
    qc.invalidateQueries({ queryKey: ["marketplace", "product", product.slug] })
    qc.invalidateQueries({ queryKey: ["marketplace", "products"] })
  }

  const createMut = useMutation({
    mutationFn: () =>
      portal.createProductReview(product.id, { rating, title: title.trim() || undefined, body: body.trim() }),
    onSuccess: () => {
      showToast(t("toast.reviewSubmitted"), "success")
      // We now hold the review ourselves: edit/delete from here on.
      setMode("edit")
      setKnownTitle(title.trim())
      setNotice(t("reviews.pendingNote"))
      invalidate()
      onChanged()
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.code === "DUPLICATE") {
        // Already reviewed this product: swap the form into edit mode.
        setMode("edit")
        setNotice(t("reviews.duplicateNote"))
        toastError(e, t("reviews.duplicateNote"))
      } else {
        toastError(e)
      }
    },
  })

  const updateMut = useMutation({
    mutationFn: () => {
      const cleanTitle = title.trim()
      // Merge semantics: a title the author did not touch is left out
      // so the stored one survives; a cleared known title clears it.
      const sendTitle = cleanTitle !== "" || (knownTitle !== undefined && cleanTitle !== knownTitle)
      return portal.updateProductReview(product.id, {
        ...(sendTitle ? { title: cleanTitle } : {}),
        body: body.trim(),
      })
    },
    onSuccess: () => {
      showToast(t("toast.reviewUpdated"), "success")
      setKnownTitle(title.trim())
      invalidate()
      onChanged()
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.status === 404) {
        // The review we thought we had is not there — back to write.
        setMode("write")
        setKnownTitle(undefined)
        setNotice(null)
      }
      toastError(e)
    },
  })

  const deleteMut = useMutation({
    mutationFn: () => portal.deleteProductReview(product.id),
    onSuccess: () => {
      showToast(t("toast.reviewRemoved"), "success")
      setMode("write")
      setRating(0)
      setTitle("")
      setBody("")
      setKnownTitle(undefined)
      setNotice(null)
      setConfirmingDelete(false)
      invalidate()
      onChanged()
    },
    onError: (e: Error) => {
      if (e instanceof ApiError && e.status === 404) {
        setMode("write")
        setKnownTitle(undefined)
        setNotice(null)
        setConfirmingDelete(false)
      }
      toastError(e)
    },
  })

  if (loading) return null
  if (!user) {
    return (
      <div className="flex flex-wrap items-center gap-3">
        <p className="text-sm text-muted-foreground">{t("reviews.signInPrompt")}</p>
        <Button asChild variant="outline" size="sm">
          <Link to="/login">{t("marketplace.signIn")}</Link>
        </Button>
      </div>
    )
  }

  const pending = createMut.isPending || updateMut.isPending || deleteMut.isPending

  const submit = () => {
    const cleanTitle = title.trim()
    const cleanBody = body.trim()
    if (mode === "write" && rating < 1) return showToast(t("reviews.errRating"), "error")
    if (!cleanBody) return showToast(t("reviews.errBody"), "error")
    if (cleanTitle.length > 120) return showToast(t("reviews.errTitleTooLong"), "error")
    if (cleanBody.length > 4000) return showToast(t("reviews.errBodyTooLong"), "error")
    if (mode === "write") createMut.mutate()
    else updateMut.mutate()
  }

  return (
    <div className="space-y-4">
      <h3 className="text-sm font-semibold">{mode === "write" ? t("reviews.writeTitle") : t("reviews.editTitle")}</h3>
      {notice && <p className="text-sm text-muted-foreground">{notice}</p>}
      <form
        onSubmit={(e) => {
          e.preventDefault()
          submit()
        }}
        className="space-y-4"
      >
        {mode === "write" && (
          <div className="space-y-2">
            <Label>{t("reviews.ratingLabel")}</Label>
            <StarRatingInput value={rating} onChange={setRating} disabled={pending} />
          </div>
        )}
        <div className="space-y-2">
          <Label htmlFor="review-title">{t("reviews.titleLabel")}</Label>
          <Input
            id="review-title"
            value={title}
            maxLength={120}
            placeholder={t("reviews.titlePlaceholder")}
            onChange={(e) => setTitle(e.target.value)}
            disabled={pending}
          />
          {mode === "edit" && knownTitle === undefined && (
            <p className="text-xs text-muted-foreground">{t("reviews.titleKeepHint")}</p>
          )}
        </div>
        <div className="space-y-2">
          <Label htmlFor="review-body">{t("reviews.bodyLabel")}</Label>
          <textarea
            id="review-body"
            value={body}
            maxLength={4000}
            rows={4}
            placeholder={t("reviews.bodyPlaceholder")}
            onChange={(e) => setBody(e.target.value)}
            disabled={pending}
            className="flex w-full rounded-md border border-input bg-background px-3 py-2 text-sm shadow-sm placeholder:text-muted-foreground focus-visible:outline-none focus-visible:ring-2 focus-visible:ring-ring disabled:cursor-not-allowed disabled:opacity-50"
          />
        </div>
        <div className="flex flex-wrap gap-2">
          <Button type="submit" disabled={pending}>
            {mode === "write" ? t("reviews.submit") : t("reviews.update")}
          </Button>
          {mode === "edit" && (
            <Button
              type="button"
              variant="outline"
              className="text-destructive"
              onClick={() => setConfirmingDelete(true)}
              disabled={pending}
            >
              {t("reviews.deleteMine")}
            </Button>
          )}
        </div>
      </form>

      <AlertDialog open={confirmingDelete} onOpenChange={setConfirmingDelete}>
        <AlertDialogContent>
          <AlertDialogHeader>
            <AlertDialogTitle>{t("reviews.deleteMineTitle")}</AlertDialogTitle>
            <AlertDialogDescription>{t("reviews.deleteMineDesc")}</AlertDialogDescription>
          </AlertDialogHeader>
          <div className="flex justify-end gap-2">
            <AlertDialogCancel>{t("common.cancel")}</AlertDialogCancel>
            <AlertDialogAction
              className="bg-destructive text-white hover:bg-destructive/90"
              onClick={() => deleteMut.mutate()}
            >
              {t("common.delete")}
            </AlertDialogAction>
          </div>
        </AlertDialogContent>
      </AlertDialog>
    </div>
  )
}

// RelatedRail — the "you may also like" rail: products sharing a
// category with this one (GET .../related), rendered as the same
// ProductCard the listing uses. Decorative: hidden while loading, on
// error, and when the product has no neighbours.
function RelatedRail({ product }: { product: MarketplaceProduct }) {
  const { t } = useI18n()
  const query = useQuery({
    queryKey: ["marketplace", "product", product.slug, "related"],
    queryFn: () => marketplace.related(product.slug),
  })
  const products = query.data?.products || []
  if (query.isLoading || query.isError || products.length === 0) return null
  return (
    <div className="space-y-4">
      <h2 className="text-xl font-semibold tracking-tight">{t("marketplace.related")}</h2>
      <div className="grid grid-cols-1 gap-4 sm:grid-cols-2 lg:grid-cols-4">
        {products.map((p) => (
          <ProductCard key={p.id} product={p} />
        ))}
      </div>
    </div>
  )
}

// formatBytes renders a file size as a plain number plus a standard unit
// abbreviation (KB/MB/…). Display-only, not money — see lib/money.ts for
// the integer-minor-unit currency helpers.
function formatBytes(bytes: number): string {
  if (!Number.isFinite(bytes) || bytes <= 0) return "0 B"
  const units = ["B", "KB", "MB", "GB", "TB"]
  const exp = Math.min(Math.floor(Math.log(bytes) / Math.log(1024)), units.length - 1)
  const value = bytes / 1024 ** exp
  return `${exp === 0 ? value : value.toFixed(2)} ${units[exp]}`
}

// shortSha shows enough of a checksum to compare two rows; the full
// value is on the cell's title.
function shortSha(sha: string): string {
  return sha.length > 16 ? `${sha.slice(0, 8)}…${sha.slice(-4)}` : sha
}
