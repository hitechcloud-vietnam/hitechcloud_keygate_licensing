import { useQuery } from "@tanstack/react-query"
import { AlertCircle, Minus, Plus, ShoppingCart, Tag, Trash2 } from "lucide-react"
import { useState } from "react"
import { Link, useLocation, useNavigate } from "react-router-dom"
import { Button } from "@/components/ui/button"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { Input } from "@/components/ui/input"
import { Label } from "@/components/ui/label"
import { Select, SelectContent, SelectItem, SelectTrigger, SelectValue } from "@/components/ui/select"
import { Separator } from "@/components/ui/separator"
import { useI18n } from "@/i18n"
import type { CheckoutQuoteRequest } from "@/lib/api"
import { ApiError, checkout } from "@/lib/api"
import { attributionFromSearch, attributionQuery } from "@/lib/attribution"
import { encodeCartItems, useCart } from "@/lib/cart"
import { COUNTRY_CODES, countryDisplayName } from "@/lib/countries"
import { formatBps, formatMinor } from "@/lib/money"

// Cart page (plan §23). The cart itself is a localStorage list of
// plan lines (lib/cart.ts) whose amounts are display hints — the
// server re-prices every line and its numbers win. The totals below
// are therefore the SERVER's quote, fetched live from
// POST /checkout/quote (multi-item), not a sum of the hints.
//
// "Proceed to checkout" hands the plan/quantity pairs to /checkout
// through the URL (?items=plan:qty,…): localStorage does not follow
// the browser across the 5-domain split, and the checkout page must
// still find the cart on a dedicated payments host.
export default function CartPage() {
  const { t, locale } = useI18n()
  const { items, setQuantity, remove, clear } = useCart()
  const { search } = useLocation()
  const navigate = useNavigate()

  const [country, setCountry] = useState("")
  const [couponInput, setCouponInput] = useState("")
  const [committedCoupon, setCommittedCoupon] = useState("")
  const [couponError, setCouponError] = useState("")

  const quote = useQuery({
    queryKey: ["cart-quote", items.map((i) => `${i.plan_id}x${i.quantity}`).join(","), country, committedCoupon],
    enabled: items.length > 0,
    queryFn: async () => {
      setCouponError("")
      const base: CheckoutQuoteRequest = {
        items: items.map((i) => ({ plan_id: i.plan_id, quantity: i.quantity })),
        country,
        region: "",
        tax_inclusive: false,
      }
      try {
        return await checkout.quote({ ...base, coupon_code: committedCoupon || undefined })
      } catch (e) {
        // A refused coupon must never hide the totals: put the refusal
        // next to the coupon field and price the cart without it.
        if (committedCoupon && e instanceof ApiError && e.status === 400) {
          setCouponError(e.message)
          return await checkout.quote({ ...base })
        }
        throw e
      }
    },
  })

  const proceed = () => {
    const qs = new URLSearchParams()
    qs.set("cart", "1")
    const encoded = encodeCartItems(items)
    if (encoded) qs.set("items", encoded)
    if (committedCoupon) qs.set("coupon_code", committedCoupon)
    if (country) qs.set("country", country)
    // The attribution the visitor arrived with rides along — the
    // /pay hand-off at the end of checkout is where it is recorded.
    const attribution = attributionQuery(attributionFromSearch(search))
    const query = [qs.toString(), attribution].filter(Boolean).join("&")
    navigate(`/checkout${query ? `?${query}` : ""}`)
  }

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("cart.title")}</h1>
        <p className="text-muted-foreground">{t("cart.subtitle")}</p>
      </div>

      {items.length === 0 ? (
        <Card>
          <CardContent className="py-12 text-center space-y-4">
            <ShoppingCart className="mx-auto h-10 w-10 text-muted-foreground" aria-hidden="true" />
            <p className="text-muted-foreground">{t("cart.empty")}</p>
            <Button asChild variant="outline">
              <Link to="/marketplace">{t("cart.emptyCta")}</Link>
            </Button>
          </CardContent>
        </Card>
      ) : (
        <div className="grid grid-cols-1 gap-6 lg:grid-cols-3">
          <div className="space-y-4 lg:col-span-2">
            <Card>
              <CardHeader className="flex-row items-center justify-between space-y-0">
                <CardTitle className="text-base">{t("cart.title")}</CardTitle>
                <Button variant="ghost" size="sm" className="text-muted-foreground" onClick={clear}>
                  <Trash2 className="h-4 w-4 mr-1" /> {t("cart.clear")}
                </Button>
              </CardHeader>
              <CardContent className="space-y-3">
                {items.map((item) => (
                  <div key={item.plan_id} className="flex flex-wrap items-center gap-3 border-b pb-3 last:border-0">
                    <div className="min-w-0 flex-1">
                      <p className="font-medium">{item.product_name}</p>
                      <p className="text-xs text-muted-foreground">{item.plan_name}</p>
                      <p className="mt-1 text-xs text-muted-foreground">
                        {t("orders.colUnitPrice")}:{" "}
                        {item.currency ? formatMinor(item.unit_amount_minor, item.currency) : "—"}
                      </p>
                    </div>
                    <div className="flex items-center gap-1">
                      {/* A stepper, not a text box: quantity is a count,
                          and one tap is the common case. */}
                      <Button
                        variant="outline"
                        size="icon"
                        className="h-8 w-8"
                        aria-label={t("cart.decreaseQty", { plan: item.plan_name })}
                        onClick={() => setQuantity(item.plan_id, item.quantity - 1)}
                      >
                        <Minus className="h-3.5 w-3.5" />
                      </Button>
                      <span className="w-8 text-center text-sm tabular-nums" aria-live="polite">
                        {item.quantity}
                      </span>
                      <Button
                        variant="outline"
                        size="icon"
                        className="h-8 w-8"
                        aria-label={t("cart.increaseQty", { plan: item.plan_name })}
                        onClick={() => setQuantity(item.plan_id, item.quantity + 1)}
                      >
                        <Plus className="h-3.5 w-3.5" />
                      </Button>
                    </div>
                    <div className="w-28 text-right text-sm font-medium">
                      {item.currency ? formatMinor(item.unit_amount_minor * item.quantity, item.currency) : "—"}
                    </div>
                    <Button
                      variant="ghost"
                      size="icon"
                      className="h-8 w-8 text-muted-foreground"
                      aria-label={t("cart.remove", { plan: item.plan_name })}
                      onClick={() => remove(item.plan_id)}
                    >
                      <Trash2 className="h-4 w-4" />
                    </Button>
                  </div>
                ))}
              </CardContent>
            </Card>

            <p className="text-xs text-muted-foreground">{t("cart.hintPrices")}</p>
          </div>

          <Card className="h-fit">
            <CardHeader>
              <CardTitle className="text-base">{t("checkout.details")}</CardTitle>
            </CardHeader>
            <CardContent className="space-y-4">
              <div className="space-y-2">
                <Label htmlFor="cart-coupon">{t("orders.coupon")}</Label>
                <div className="flex gap-2">
                  <Input
                    id="cart-coupon"
                    value={couponInput}
                    onChange={(e) => setCouponInput(e.target.value)}
                    placeholder={t("checkout.couponPlaceholder")}
                    onKeyDown={(e) => {
                      if (e.key === "Enter") setCommittedCoupon(couponInput.trim())
                    }}
                  />
                  <Button variant="outline" onClick={() => setCommittedCoupon(couponInput.trim())} className="shrink-0">
                    <Tag className="h-4 w-4 mr-1" /> {t("checkout.couponApply")}
                  </Button>
                </div>
                {couponError && <p className="text-xs text-destructive">{couponError}</p>}
              </div>

              <div className="space-y-2">
                <Label htmlFor="cart-country">{t("checkout.countryLabel")}</Label>
                <Select value={country} onValueChange={setCountry}>
                  <SelectTrigger id="cart-country" className="w-full">
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

              <Separator />

              {quote.isLoading && <div className="h-20 animate-pulse bg-muted rounded-lg" />}
              {quote.isError && (
                <div className="flex items-start gap-2 text-sm text-destructive">
                  <AlertCircle className="h-4 w-4 mt-0.5 shrink-0" />
                  <div>
                    <p>{t("cart.quoteError")}</p>
                    <Button variant="outline" size="sm" className="mt-2" onClick={() => quote.refetch()}>
                      {t("common.retry")}
                    </Button>
                  </div>
                </div>
              )}
              {quote.data && (
                <div className="space-y-2 text-sm">
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">{t("orders.subtotal")}</span>
                    {/* Multi-currency (§53): the quote's currency names
                        every total — never a display guess. */}
                    <span>{formatMinor(quote.data.subtotal_minor, quote.data.currency)}</span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">{t("orders.discount")}</span>
                    <span className={quote.data.discount_minor > 0 ? "text-emerald-600" : ""}>
                      {quote.data.discount_minor > 0 ? "-" : ""}
                      {formatMinor(quote.data.discount_minor, quote.data.currency)}
                    </span>
                  </div>
                  <div className="flex justify-between">
                    <span className="text-muted-foreground">{t("orders.tax")}</span>
                    <span>{formatMinor(quote.data.tax_minor, quote.data.currency)}</span>
                  </div>
                  {quote.data.tax_rates && quote.data.tax_rates.length > 0 && (
                    <div className="space-y-0.5 pl-1 text-xs text-muted-foreground">
                      {quote.data.tax_rates.map((r) => (
                        <div key={r.jurisdiction} className="flex justify-between">
                          <span>{r.jurisdiction}</span>
                          <span>{formatBps(r.basis_points)}</span>
                        </div>
                      ))}
                    </div>
                  )}
                  <Separator />
                  <div className="flex justify-between text-base font-medium">
                    <span>{t("orders.colTotal")}</span>
                    <span>{formatMinor(quote.data.total_minor, quote.data.currency)}</span>
                  </div>
                  {quote.data.applied_coupon && (
                    <p className="text-xs text-emerald-600">
                      {t("checkout.couponApplied")}: {quote.data.applied_coupon.code}
                    </p>
                  )}
                </div>
              )}

              <Button className="w-full" size="lg" onClick={proceed} disabled={items.length === 0}>
                <ShoppingCart className="h-4 w-4 mr-2" /> {t("cart.proceed")}
              </Button>
              <Button asChild variant="ghost" className="w-full">
                <Link to="/marketplace">{t("cart.continueShopping")}</Link>
              </Button>
            </CardContent>
          </Card>
        </div>
      )}
    </div>
  )
}
