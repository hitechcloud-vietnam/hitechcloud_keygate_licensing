// Checkout attribution (plan Phase 7): who brought the sale. A
// visitor may arrive carrying a reseller code and/or an affiliate
// referral code — as ?reseller_code= / ?ref= on the current URL, or as
// the first-party htc_ref cookie the /r/<code> redirect sets. Whatever
// they arrived with has to survive the in-app hops (marketplace →
// product → checkout) and reach the request the browser finally issues
// to the server /pay/:checkout_id route, which is where attribution is
// recorded and wholesale pricing is resolved (see
// internal/payment/attribution.go for the server-side contract):
//
//   GET /pay/:checkout_id?reseller_code=&ref=&email=
//
//   - reseller_code  the reseller's folded contact email. Attribution
//     only — it never changes what is charged.
//   - ref            the affiliate referral code; the server also
//     falls back to the htc_ref cookie, but we forward explicitly so
//     the payment request carries the full story on its own.
//   - email          the buyer's address — the wholesale authority. The
//     logged-in user's email when known.

// ReferralCookieName mirrors handler.ReferralCookieName and the name
// documented on the affiliates migration: the cookie /r/<code> sets.
export const REFERRAL_COOKIE = "htc_ref"

export interface CheckoutAttribution {
  reseller_code?: string
  ref?: string
  email?: string
}

// readReferralCookie returns the folded referral code the /r/<code>
// redirect left behind, or "" — a missing or junk cookie is simply no
// attribution, never a sentinel.
export function readReferralCookie(): string {
  for (const part of document.cookie.split(";")) {
    const [name, ...rest] = part.split("=")
    if (name.trim() === REFERRAL_COOKIE) {
      try {
        return decodeURIComponent(rest.join("=")).trim()
      } catch {
        return rest.join("=").trim()
      }
    }
  }
  return ""
}

// attributionFromSearch collects what the current URL carries, with
// the cookie standing in for a missing ?ref. email falls back to the
// signed-in user's address (wholesale pricing keys off it), unless the
// link itself named one — a link's explicit value is a deliberate
// choice. The caller passes `userEmail` as undefined when nobody is
// signed in.
export function attributionFromSearch(search: string, userEmail?: string): CheckoutAttribution {
  const params = new URLSearchParams(search)
  const attr: CheckoutAttribution = {}
  const reseller = (params.get("reseller_code") || "").trim()
  if (reseller) attr.reseller_code = reseller
  const ref = (params.get("ref") || readReferralCookie() || "").trim()
  if (ref) attr.ref = ref
  const email = (params.get("email") || userEmail || "").trim()
  if (email) attr.email = email
  return attr
}

// attributionQuery renders the attribution as a query string (without
// the leading "?"), "" when there is nothing to carry. Both the /pay
// hand-off and the in-app checkout links build on it, so every hop
// spells the attribution the same way.
export function attributionQuery(attr: CheckoutAttribution): string {
  const qs = new URLSearchParams()
  if (attr.reseller_code) qs.set("reseller_code", attr.reseller_code)
  if (attr.ref) qs.set("ref", attr.ref)
  if (attr.email) qs.set("email", attr.email)
  return qs.toString()
}

// withAttribution appends attribution to a path that may already carry
// its own query (the checkout page passes coupon_code and country
// along with it).
export function withAttribution(path: string, attr: CheckoutAttribution): string {
  const q = attributionQuery(attr)
  if (!q) return path
  return `${path}${path.includes("?") ? "&" : "?"}${q}`
}
