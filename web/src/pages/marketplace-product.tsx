import { useQuery } from "@tanstack/react-query"
import { AlertCircle, ArrowLeft, BookOpen, Download, Github, Globe, Package } from "lucide-react"
import { Link, useParams } from "react-router-dom"
import { Badge } from "@/components/ui/badge"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Separator } from "@/components/ui/separator"
import { useI18n } from "@/i18n"
import type { MarketplaceProduct } from "@/lib/api"
import { ApiError, marketplace } from "@/lib/api"
import { formatMinor } from "@/lib/money"
import { formatDate } from "@/lib/utils"

// The public product page (plan §30 + Phase 6): GET
// /marketplace/products/:slug → data.product. An unknown slug answers
// 404 exactly like a failed lookup, so we show one friendly not-found
// state. The §223 catalog enrichment fields (description, logo, images,
// doc/website/repo URLs, vendor) are rendered when present and
// tolerated when null/absent — the API may not carry them yet.
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
                            {p.checkout_id ? (
                              <Button asChild size="sm">
                                <Link to={`/checkout/${p.checkout_id}`}>{t("marketplace.buy")}</Link>
                              </Button>
                            ) : (
                              <span className="text-xs text-muted-foreground">—</span>
                            )}
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
