import { useQuery } from "@tanstack/react-query"
import { AlertCircle, CreditCard, Tag } from "lucide-react"
import { useState } from "react"
import { useParams } from "react-router-dom"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Separator } from "@/components/ui/separator"
import { useSiteConfig } from "@/hooks/use-site-config"
import { type TranslationKeys, useI18n } from "@/i18n"
import type { CheckoutQuoteRequest, CheckoutQuoteResult } from "@/lib/api"
import { ApiError, checkout } from "@/lib/api"
import { COUNTRY_CODES, countryDisplayName } from "@/lib/countries"
import { formatBps, formatMinor } from "@/lib/money"

// A checkout prices its one item — named by the checkout_id in the URL —
// with a coupon and the buyer's country tax, then hands off to the
// server /pay/:checkout_id route (a 302 to Stripe). Nothing here is
// written until the buyer is on that payment page: the quote is a
// preview, and every amount is integer minor units.
export default function CheckoutPage() {
  const { t, locale } = useI18n()
  const { site_name, logo_url, attribution_text, attribution_url } = useSiteConfig()
  const { checkout_id: checkoutId = "" } = useParams()

  const [country, setCountry] = useState("")
  const [couponInput, setCouponInput] = useState("")
  const [committedCoupon, setCommittedCoupon] = useState("")
  const [couponError, setCouponError] = useState("")

  const quote = useQuery({
    queryKey: ["checkout-quote", checkoutId, country, committedCoupon],
    enabled: !!checkoutId,
    queryFn: async () => {
      setCouponError("")
      const base: CheckoutQuoteRequest = {
        items: [{ checkout_id: checkoutId, quantity: 1 }],
        country,
        region: "",
        tax_inclusive: false,
      }
      try {
        return await checkout.quote({ ...base, coupon_code: committedCoupon || undefined })
      } catch (e) {
        // A coupon the server refuses must never hide the totals: put
        // the refusal next to the coupon field and price the order
        // without it, so the buyer always sees what they would pay.
        if (committedCoupon && e instanceof ApiError && e.status === 400) {
          setCouponError(e.message)
          return await checkout.quote({ ...base })
        }
        throw e
      }
    },
  })

  const applyCoupon = () => {
    setCommittedCoupon(couponInput.trim())
  }

  const pay = () => {
    const qs = new URLSearchParams()
    if (committedCoupon) qs.set("coupon_code", committedCoupon)
    if (country) qs.set("country", country)
    const s = qs.toString()
    // /pay/:checkout_id is a server route (a 302 to Stripe), not a React
    // page — navigate the browser there rather than routing in-app.
    window.location.assign(`/pay/${checkoutId}${s ? `?${s}` : ""}`)
  }

  return (
    <div className="min-h-screen bg-background flex flex-col">
      <header className="border-b bg-card">
        <div className="max-w-3xl mx-auto flex items-center gap-2 h-14 px-4">
          <img src={logo_url || "/logo.svg"} alt={site_name} className="h-6 w-6" />
          <span className="font-bold text-lg tracking-tight">{site_name}</span>
        </div>
      </header>

      <main className="flex-1 max-w-3xl w-full mx-auto p-4 md:p-8">
        <div className="space-y-6">
          <div>
            <h1 className="text-2xl font-bold tracking-tight">{t("checkout.pageTitle")}</h1>
            <p className="text-muted-foreground">{t("checkout.pageSubtitle")}</p>
          </div>

          {!checkoutId ? (
            <Card>
              <CardContent className="py-12 text-center text-muted-foreground">{t("checkout.missingId")}</CardContent>
            </Card>
          ) : quote.isLoading ? (
            <div className="space-y-4">
              <div className="h-48 animate-pulse bg-muted rounded-lg" />
              <div className="h-64 animate-pulse bg-muted rounded-lg" />
            </div>
          ) : quote.isError || !quote.data ? (
            <Card>
              <CardContent className="py-10 text-center space-y-4">
                <div className="flex items-center justify-center gap-2 text-destructive">
                  <AlertCircle className="h-5 w-5" />
                  <span>{t("checkout.loadError")}</span>
                </div>
                <p className="text-sm text-muted-foreground">
                  {quote.error instanceof Error ? quote.error.message : ""}
                </p>
                <Button variant="outline" onClick={() => quote.refetch()}>
                  {t("common.retry")}
                </Button>
              </CardContent>
            </Card>
          ) : (
            <CheckoutBody
              data={quote.data}
              country={country}
              onCountry={setCountry}
              locale={locale}
              couponInput={couponInput}
              onCouponInput={setCouponInput}
              onApplyCoupon={applyCoupon}
              couponError={couponError}
              onPay={pay}
            />
          )}
        </div>
      </main>

      {/* Attribution required by AGPL v3 Section 7(b) — see NOTICE */}
      <footer className="border-t py-3 text-center">
        <a
          href={attribution_url}
          target="_blank"
          rel="noopener noreferrer"
          className="text-xs text-muted-foreground/50 hover:text-muted-foreground transition-colors"
        >
          {attribution_text}
        </a>
      </footer>
    </div>
  )
}

function CheckoutBody({
  data,
  country,
  onCountry,
  locale,
  couponInput,
  onCouponInput,
  onApplyCoupon,
  couponError,
  onPay,
}: {
  data: CheckoutQuoteResult
  country: string
  onCountry: (v: string) => void
  locale: string
  couponInput: string
  onCouponInput: (v: string) => void
  onApplyCoupon: () => void
  couponError: string
  onPay: () => void
}) {
  const { t } = useI18n()
  const lines = data.lines || []
  const currency = data.currency

  return (
    <>
      <Card>
        <CardHeader>
          <CardTitle className="text-base">{t("checkout.orderSummary")}</CardTitle>
        </CardHeader>
        <CardContent className="space-y-3">
          {lines.map((l) => (
            <div
              key={`${l.description || l.sku || l.plan_id || "line"}-${l.quantity}-${l.unit_amount_minor}`}
              className="flex items-start justify-between gap-3 text-sm"
            >
              <div className="min-w-0">
                <p className="font-medium">{l.description || l.sku || l.plan_id || "-"}</p>
                <p className="text-xs text-muted-foreground">
                  {t("orders.colQty")}: {l.quantity} · {t("orders.colUnitPrice")}:{" "}
                  {formatMinor(l.unit_amount_minor, currency)}
                </p>
              </div>
              <div className="text-right">
                <div className="font-medium">{formatMinor(l.line_total_minor, currency)}</div>
                {l.line_discount_minor > 0 && (
                  <div className="text-xs text-emerald-600">-{formatMinor(l.line_discount_minor, currency)}</div>
                )}
              </div>
            </div>
          ))}
          {lines.length === 0 && <p className="text-sm text-muted-foreground">{t("checkout.emptyLines")}</p>}
        </CardContent>
      </Card>

      <div className="grid grid-cols-1 gap-6 md:grid-cols-2">
        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("checkout.details")}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-4">
            <div className="space-y-2">
              <Label htmlFor="coupon">{t("orders.coupon")}</Label>
              <div className="flex gap-2">
                <Input
                  id="coupon"
                  value={couponInput}
                  onChange={(e) => onCouponInput(e.target.value)}
                  placeholder={t("checkout.couponPlaceholder")}
                  onKeyDown={(e) => {
                    if (e.key === "Enter") onApplyCoupon()
                  }}
                />
                <Button variant="outline" onClick={onApplyCoupon} className="shrink-0">
                  <Tag className="h-4 w-4 mr-1" /> {t("checkout.couponApply")}
                </Button>
              </div>
              {couponError && <p className="text-xs text-destructive">{couponError}</p>}
              {data.applied_coupon && (
                <p className="text-xs text-emerald-600">
                  {t("checkout.couponApplied")}: {data.applied_coupon.code} (
                  {couponTypeLabel(t, data.applied_coupon.type)} ·{" "}
                  {data.applied_coupon.type === "percent_off"
                    ? formatBps(data.applied_coupon.value)
                    : formatMinor(data.applied_coupon.value, data.applied_coupon.currency || currency)}
                  )
                </p>
              )}
            </div>

            <div className="space-y-2">
              <Label htmlFor="country">{t("checkout.countryLabel")}</Label>
              <Select value={country} onValueChange={onCountry}>
                <SelectTrigger id="country" className="w-full">
                  <SelectValue placeholder={t("checkout.countryPlaceholder")} />
                </SelectTrigger>
                <SelectContent>
                  {COUNTRY_CODES.map((code) => (
                    <SelectItem key={code} value={code}>
                      {countryDisplayName(code, locale)}
                    </SelectItem>
                  ))}
                </SelectContent>
              </Select>
            </div>
          </CardContent>
        </Card>

        <Card>
          <CardHeader>
            <CardTitle className="text-base">{t("checkout.totals")}</CardTitle>
          </CardHeader>
          <CardContent className="space-y-2 text-sm">
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t("orders.subtotal")}</span>
              <span>{formatMinor(data.subtotal_minor, currency)}</span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">{t("orders.discount")}</span>
              <span className={data.discount_minor > 0 ? "text-emerald-600" : ""}>
                {data.discount_minor > 0 ? "-" : ""}
                {formatMinor(data.discount_minor, currency)}
              </span>
            </div>
            <div className="flex justify-between">
              <span className="text-muted-foreground">
                {t("orders.tax")}
                <span className="ml-1 text-xs">
                  ({data.tax_inclusive ? t("orders.taxInclusive") : t("orders.taxExclusive")})
                </span>
              </span>
              <span>{formatMinor(data.tax_minor, currency)}</span>
            </div>
            {data.tax_rates && data.tax_rates.length > 0 && (
              <div className="space-y-0.5 text-xs text-muted-foreground pl-1">
                {data.tax_rates.map((r) => (
                  <div key={r.jurisdiction} className="flex justify-between">
                    <span>{r.jurisdiction}</span>
                    <span>{formatBps(r.basis_points)}</span>
                  </div>
                ))}
              </div>
            )}
            <Separator />
            <div className="flex justify-between font-medium text-base">
              <span>{t("orders.colTotal")}</span>
              <span>{formatMinor(data.total_minor, currency)}</span>
            </div>
            <Button className="w-full mt-3" size="lg" onClick={onPay}>
              <CreditCard className="h-4 w-4 mr-2" /> {t("checkout.pay")}
            </Button>
          </CardContent>
        </Card>
      </div>
    </>
  )
}

// An applied coupon records its type in the pricing engine's spelling
// ("percent_off" / "fixed_amount_off"); both fixed spellings read the
// same to the buyer.
function couponTypeLabel(t: (key: TranslationKeys) => string, type: string): string {
  if (type === "percent_off") return t("coupons.percentOff")
  if (type === "fixed_off" || type === "fixed_amount_off") return t("coupons.fixedOff")
  return type
}
