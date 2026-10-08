const BASE = `${import.meta.env.VITE_API_URL || ""}/api/v1`

// ServiceUnavailableError means the server could not be reached or could
// not answer right now: a network failure, a 429 or a 5xx — including a
// session renewal that failed for one of those reasons. The user may
// well still be signed in, so it must never be read as a sign-out; the
// auth layer retries instead (see AuthProvider).
export class ServiceUnavailableError extends Error {
  // The X-Request-ID the response echoed back ("" when the request
  // never produced one), so a toast can hand support a reference.
  requestId: string
  constructor(message: string, requestId = "") {
    super(message)
    this.requestId = requestId
  }
}

// ApiError is a request the server answered with a structured error. It
// carries the machine-readable error code beside the human message so a
// page can route the message to the right place — a coupon refusal goes
// next to the coupon field, not into a generic toast. requestId is the
// X-Request-ID the server echoed back, for error reporting (§90).
export class ApiError extends Error {
  code: string
  status: number
  requestId: string
  constructor(message: string, status: number, code = "", requestId = "") {
    super(message)
    this.status = status
    this.code = code
    this.requestId = requestId
  }
}

type RefreshOutcome = "ok" | "denied" | "unavailable"

// The session cookie lives 24 hours; the refresh cookie 30 days. When a
// call comes back 401, renew the session once and retry it. Only one
// refresh runs per page at a time: the server rotates the refresh token
// on every use, so parallel calls would present the same token twice.
// (The server also tolerates a just-rotated token for a few seconds,
// which covers several tabs renewing at the same moment.)
let refreshing: Promise<RefreshOutcome> | null = null

function refreshSession(): Promise<RefreshOutcome> {
  refreshing ??= fetch(`${BASE}/auth/refresh`, { method: "POST", credentials: "include" })
    // Only 401/403 mean the refresh cookie is no good. A 429 (shared
    // rate limit) or a 5xx is a hiccup: the user is still signed in.
    .then((r): RefreshOutcome => (r.ok ? "ok" : r.status === 401 || r.status === 403 ? "denied" : "unavailable"))
    .catch((): RefreshOutcome => "unavailable")
    .finally(() => {
      refreshing = null
    })
  return refreshing
}

async function request<T>(path: string, opts?: RequestInit, retried = false): Promise<T> {
  let res: Response
  try {
    res = await fetch(BASE + path, {
      credentials: "include",
      headers: { "Content-Type": "application/json", ...opts?.headers },
      ...opts,
    })
  } catch (e) {
    // Network-level failure (server down, DNS, CORS). fetch() throws
    // a TypeError here; we surface a friendly message instead of
    // "Failed to fetch" which is meaningless to end users.
    const reason = e instanceof Error ? e.message : String(e)
    throw new ServiceUnavailableError(`Network error: ${reason}. Is the server reachable?`)
  }

  // Session expired: renew it and retry the call once. Auth endpoints
  // answer 401 for their own reasons (a wrong code, no refresh cookie),
  // so they are not retried — that would mask the error or loop. Logout
  // is the exception: it needs a live session to revoke the refresh
  // token, and if it fails the login page would silently renew the
  // session and sign the user straight back in.
  const renewable = !path.startsWith("/auth/") || path === "/auth/logout"
  if (res.status === 401 && !retried && renewable) {
    const outcome = await refreshSession()
    if (outcome === "ok") return request<T>(path, opts, true)
    if (outcome === "unavailable") {
      // Not a sign-out: the session may well still be renewable.
      throw new ServiceUnavailableError("Could not renew your session right now. Check your connection and try again.")
    }
  }

  // Could not renew: redirect to login
  if (res.status === 401) {
    // Don't redirect if already on login page or fetching auth state
    if (!window.location.pathname.startsWith("/login") && path !== "/portal/me") {
      window.location.href = "/login"
    }
    throw new Error("Session expired")
  }

  if (res.status === 204) return undefined as T

  // The middleware's request id, echoed on every response. It is the
  // reference an error toast quotes so a failure can be traced in the
  // server logs (plan §90).
  const requestId = res.headers.get("X-Request-ID") || ""

  // Read the body as text first so an empty or non-JSON response (502
  // from a proxy, server crash mid-response, HTML error page, etc.)
  // doesn't produce the cryptic "Unexpected end of JSON input" — that
  // error confuses users who clicked a normal button.
  const raw = await res.text()
  let json: any = null
  if (raw) {
    try {
      json = JSON.parse(raw)
    } catch {
      // body wasn't JSON — keep `json` null, fall through to text path
    }
  }

  if (!res.ok) {
    const msg =
      json?.error?.message ||
      (typeof json?.error === "string" ? json.error : null) ||
      (raw && raw.length < 200 ? raw : null) ||
      `Request failed (${res.status}${res.statusText ? ` ${res.statusText}` : ""})`
    // A 429 or 5xx says nothing about the session: the server is busy or
    // failing. Callers that decide "signed in or not" must tell it apart.
    const code = typeof json?.error?.code === "string" ? json.error.code : ""
    if (res.status === 429 || res.status >= 500) throw new ServiceUnavailableError(msg, requestId)
    throw new ApiError(msg, res.status, code, requestId)
  }
  return (json?.data !== undefined ? json.data : json) as T
}

function get<T>(path: string) {
  return request<T>(path)
}
function post<T>(path: string, body?: unknown) {
  return request<T>(path, { method: "POST", body: body ? JSON.stringify(body) : undefined })
}
function put<T>(path: string, body?: unknown) {
  return request<T>(path, { method: "PUT", body: body ? JSON.stringify(body) : undefined })
}
function patch<T>(path: string, body?: unknown) {
  return request<T>(path, { method: "PATCH", body: body ? JSON.stringify(body) : undefined })
}
function del<T>(path: string) {
  return request<T>(path, { method: "DELETE" })
}

// ─── Site Config (public, no auth) ───
export const site = {
  config: () => get<SiteConfigResponse>("/config"),
}

// ─── Auth ───
// First run setup. Public: the server refuses initialize once an owner exists.
export const setup = {
  status: () => get<{ needed: boolean; step: string }>("/setup/status"),
  initialize: (body: {
    admin_name: string
    admin_email: string
    site_name: string
    product_name: string
    product_slug: string
    product_type: "desktop" | "saas" | "hybrid"
  }) => post<{ user: unknown; product: unknown; plan: unknown }>("/setup/initialize", body),
}

export const auth = {
  me: () =>
    get<{ id: string; email: string; name: string; avatar_url: string; is_admin: boolean; role: string }>("/portal/me"),
  providers: () => get<{ dev_login: boolean; otp: boolean }>("/auth/providers"),
  logout: () => post<void>("/auth/logout"),
  devLogin: (email: string, name: string) => post<{ status: string }>("/auth/dev-login", { email, name }),
  otpSend: (email: string) => post<{ status: string }>("/auth/otp/send", { email }),
  otpVerify: (email: string, code: string) =>
    post<{ status: string; email: string; name: string; is_admin: boolean; role: string }>("/auth/otp/verify", {
      email,
      code,
    }),
}

// ─── Checkout ───
export const checkout = {
  verify: (sessionId: string) =>
    get<{ status: string; email?: string; kind?: string }>(`/checkout/verify?session_id=${sessionId}`),
  // Public pricing preview of a checkout (items + coupon + tax) before
  // payment. Unit prices are resolved server-side — there is no amount
  // field to send. The body is pinned by the checkout contract.
  quote: (body: CheckoutQuoteRequest) => post<CheckoutQuoteResult>("/checkout/quote", body),
  // Vietnamese payment gateways (plan §25): which gateways this install
  // can charge through right now (availability = credentials present).
  gatewayMethods: () => get<{ methods: GatewayMethod[] }>("/checkout/gateway-pay/methods"),
  // Start a one-off VND payment through a gateway. The server prices
  // the order itself — no amount is sent. Returns the redirect target.
  // Accepts a single plan_id (one-off checkout) or items (cart).
  gatewayPay: (body: GatewayPayRequest) => post<GatewayPayResult>("/checkout/gateway-pay", body),
  // Cart checkout: several plans in one Stripe payment. 404 until the
  // backend route lands — the cart page surfaces the failure like any
  // other pay error.
  cartPay: (body: CheckoutCartRequest) => post<CheckoutCartResult>("/checkout/cart", body),
  // Polled by the browser return page while the gateway's IPN settles
  // the order server-side.
  gatewayPayStatus: (orderNumber: string) =>
    get<{ status: string; provider: string }>(`/checkout/gateway-pay/status?order=${encodeURIComponent(orderNumber)}`),
}

// ─── Invites (public, token-only) ───
// The plain invite token IS the email-ownership proof — no session
// required. Backend collapses bad/expired/already-accepted into one
// generic error so an attacker can't probe token validity.
export const invites = {
  accept: (token: string) =>
    post<{ user_id: string; email: string; license_id: string; product_name?: string; role: string }>(
      "/invites/accept",
      { token },
    ),
}

// ─── Marketplace (public, anonymous) ───
// The storefront's read side (plan §30 + Phase 6): browse categories,
// discover products, open a product page. Anonymous and read-only.
// The API accepts only sort=name|newest (anything else is 400) and
// answers a listing as data.{products,total,limit,offset} and a
// product page as data.product. Prices are not in this payload — they
// live on Stripe — so plan cards carry price/currency as null.
export const marketplace = {
  categories: () => get<{ categories: MarketplaceCategory[] }>("/marketplace/categories"),
  products: (params?: {
    search?: string
    // The ?category= filter takes a category SLUG, not its id.
    category?: string
    sort?: string // "name" | "newest"
    order?: "asc" | "desc"
    limit?: number
    offset?: number
  }) => get<Paged<{ products: MarketplaceProduct[] }>>(`/marketplace/products?${listQuery(params)}`),
  // An unknown slug answers 404 exactly like a failed lookup.
  product: (slug: string) => get<{ product: MarketplaceProduct }>(`/marketplace/products/${encodeURIComponent(slug)}`),
  // The Reviews section of a product page: one page of the product's
  // APPROVED reviews (newest first) with the aggregate beside them —
  // data.{reviews,total,limit,offset,rating_average_bps,rating_count}.
  // bps, not a float: 10000 = 1 star, 50000 = 5.
  reviews: (slug: string, params?: { limit?: number; offset?: number }) =>
    get<Paged<{ reviews: PublicReview[] }> & { rating_average_bps: number; rating_count: number }>(
      `/marketplace/products/${encodeURIComponent(slug)}/reviews?${listQuery(params)}`,
    ),
  // The "you may also like" rail: same product cards as the listing
  // (categories, plans and rating aggregates included).
  related: (slug: string, params?: { limit?: number }) =>
    get<{ products: MarketplaceProduct[]; total: number }>(
      `/marketplace/products/${encodeURIComponent(slug)}/related?${listQuery(params)}`,
    ),
}

// ─── Portal ───
export const portal = {
  licenses: () => get<{ licenses: PortalLicense[]; renewals_enabled?: boolean }>("/portal/licenses"),
  listPlans: (productId: string) => get<{ plans: Plan[] }>(`/portal/plans?product_id=${productId}`),
  updateProfile: (data: { name: string }) =>
    put<{ id: string; email: string; name: string; avatar_url: string; role: string }>("/portal/profile", data),
  recordUsage: (data: { license_key: string; feature: string; quantity?: number }) =>
    post<{ status?: string }>("/portal/usage", data),
  quotaStatus: (data: { license_key: string; feature: string }) =>
    post<{ used?: number; limit?: number; remaining?: number }>("/portal/usage/status", data),
  // Customer-facing team management for multi-seat plans. Session-
  // authed (cookie); the body's license_key only names the target
  // license — the cookie is the actual authentication.
  // Self-service device/activation management. Backend authorises the
  // license owner OR any accepted seat (member or admin) — a teammate
  // who lost a laptop can free their own slot without a support ticket
  // (internal/handler/portal_activations.go). license_key is in the
  // path; the session cookie is the actual auth.
  listActivations: (licenseKey: string) =>
    get<{ activations: Activation[]; max: number }>(`/portal/licenses/${encodeURIComponent(licenseKey)}/activations`),
  removeActivation: (licenseKey: string, activationId: string) =>
    del<{ status: string }>(
      `/portal/licenses/${encodeURIComponent(licenseKey)}/activations/${encodeURIComponent(activationId)}`,
    ),
  listSeats: (licenseKey: string) => post<{ seats: Seat[] }>("/portal/seats", { license_key: licenseKey }),
  addSeat: (data: { license_key: string; email: string; role?: string }) => post<Seat>("/portal/seats/add", data),
  removeSeat: (data: { license_key: string; seat_id: string }) =>
    post<{ status: string }>("/portal/seats/remove", data),
  changePlan: (data: { license_id: string; new_price_id: string; prorate?: boolean }) =>
    post<{ status: string; new_plan_id: string; new_plan_name: string; proration: string }>(
      "/portal/subscription/change-plan",
      data,
    ),
  cancelSubscription: (data: { license_id: string; immediate?: boolean }) =>
    post<{ status: string; immediate: boolean }>("/portal/subscription/cancel", data),
  getBillingPortal: (data: { license_id: string }) =>
    post<{ url: string }>("/portal/subscription/billing-portal", data),
  getInvoices: (licenseId: string) =>
    get<{ invoices: Invoice[] }>(`/portal/subscription/invoices?license_id=${licenseId}`),
  renewUpdates: (data: { license_id: string }) => post<{ url: string }>("/portal/updates/renew", data),

  // ─── Portal commerce: orders, invoices, downloads (Phase 5) ───
  // The logged-in customer's own commerce records. Ownership is
  // enforced server-side against the session email; a lookup that is
  // not the customer's answers exactly like a missing one.
  listPortalOrders: (params?: { status?: string; limit?: number; offset?: number }) =>
    get<Paged<{ orders: Order[] }>>(`/portal/orders?${listQuery(params)}`),
  getPortalOrder: (id: string) => get<{ order: Order; invoices: OrderInvoice[] }>(`/portal/orders/${id}`),
  listPortalOrderInvoices: (id: string, params?: { limit?: number; offset?: number }) =>
    get<Paged<{ invoices: OrderInvoice[] }>>(`/portal/orders/${id}/invoices?${listQuery(params)}`),
  getPortalInvoice: (id: string) => get<{ invoice: OrderInvoice; order: Order }>(`/portal/invoices/${id}`),
  // Downloads answer with the whole entitled list under `downloads`
  // (not paged): what a customer may fetch is bounded by their
  // licences, not by a page size.
  listPortalDownloads: (params?: { channel?: string; platform?: string }) =>
    get<{ downloads: PortalDownload[] }>(`/portal/downloads?${listQuery(params)}`),

  // ─── Customer API keys (portal) ───
  listPortalAPIKeys: (params?: { limit?: number; offset?: number }) =>
    get<Paged<{ api_keys: CustomerAPIKey[] }>>(`/portal/api-keys?${listQuery(params)}`),
  // The plaintext secret is returned exactly once, on creation. DELETE
  // is a soft revoke (revoked_at stamped, row kept for the audit trail).
  createPortalAPIKey: (data: { name: string; scopes?: string; expires_at?: string }) =>
    post<{ api_key: CustomerAPIKey; secret: string; note: string }>("/portal/api-keys", data),
  revokePortalAPIKey: (id: string) => del<CustomerAPIKey>(`/portal/api-keys/${id}`),

  // ─── Portal webhooks (account-scoped) ───
  // The customer's own notification endpoints. Secret is returned only
  // on create. The event vocabulary is WEBHOOK_EVENTS (lib/webhook-events).
  listPortalWebhooks: (params?: { limit?: number; offset?: number }) =>
    get<Paged<{ webhooks: PortalWebhook[] }>>(`/portal/webhooks?${listQuery(params)}`),
  createPortalWebhook: (data: { url: string; events: string[]; active?: boolean }) =>
    post<{ webhook: PortalWebhook; secret: string }>("/portal/webhooks", data),
  updatePortalWebhook: (id: string, data: { url?: string; events?: string[]; active?: boolean }) =>
    patch<PortalWebhook>(`/portal/webhooks/${encodeURIComponent(id)}`, data),
  deletePortalWebhook: (id: string) => del<void>(`/portal/webhooks/${encodeURIComponent(id)}`),
  testPortalWebhook: (id: string) => post<{ status?: string }>(`/portal/webhooks/${encodeURIComponent(id)}/test`),

  // ─── Product reviews (one per product per account) ───
  // The signed-in customer's own review of one product. Create starts
  // pending (moderation publishes it) and a second create answers 409
  // DUPLICATE. PATCH edits the author's own title/body only (an
  // omitted field keeps its stored value — no rating move); DELETE
  // takes their own review down. There is no read endpoint: ownership
  // is (product, session email) and a lookup that finds nothing —
  // including somebody else's review — is a quiet 404.
  createProductReview: (productId: string, data: { rating: number; title?: string; body: string }) =>
    post<Review>(`/portal/products/${encodeURIComponent(productId)}/reviews`, data),
  updateProductReview: (productId: string, data: { title?: string; body?: string }) =>
    patch<Review>(`/portal/products/${encodeURIComponent(productId)}/reviews`, data),
  deleteProductReview: (productId: string) => del<void>(`/portal/products/${encodeURIComponent(productId)}/reviews`),

  // ─── Notification center (plan §88) ───
  // The signed-in account's in-app notifications. The human text is
  // NOT in the payload: rows carry title_key — the event name — and
  // the UI renders it through notifications.events.* (see
  // components/notification-bell). :id/read and read-all are
  // idempotent, so a click-through retry never fails.
  // sort ∈ {created_at, event, priority, read_at}; unknown keys keep
  // the default order (created_at desc).
  listNotifications: (params?: {
    unread_only?: boolean
    sort?: string
    order?: "asc" | "desc"
    limit?: number
    offset?: number
  }) =>
    get<Paged<{ notifications: NotificationItem[]; unread_count: number }>>(
      `/portal/notifications?${listQuery(params)}`,
    ),
  unreadNotifications: () => get<{ unread_count: number }>("/portal/notifications/unread-count"),
  markNotificationRead: (id: string) =>
    post<{ status?: string }>(`/portal/notifications/${encodeURIComponent(id)}/read`),
  markAllNotificationsRead: () => post<{ status?: string; updated?: number }>("/portal/notifications/read-all"),

  // ─── Privacy: data export + account deletion (plan §82) ───
  // exportAccount returns everything the platform holds about the
  // account as one JSON document; the page wraps it in a Blob for the
  // download. The payload carries API key / webhook METADATA only
  // (never secrets or hashes); license keys are included — they are
  // the account's own credentials.
  exportAccount: () => get<AccountExport>("/portal/export"),
  // deleteAccount anonymizes the account. `confirm` must be the
  // account email (compared case-insensitively) or the exact phrase
  // "DELETE MY ACCOUNT"; anything else is 400
  // PRIVACY_CONFIRMATION_MISMATCH and nothing changes. Financial
  // records are preserved server-side (see the response's `retained`).
  deleteAccount: (confirm: string) => post<AccountAnonymizeResult>("/portal/delete-account", { confirm }),
}

// ─── Admin ───
// Every list endpoint answers with its rows under their own name and
// the same three numbers beside them. `limit` is what the server
// applied, not what was asked for — it clamps.
// Builds a list request's query string. Empty and undefined values
// are left out so a filter that is not set does not become one that
// matches the empty string.
function listQuery(params?: Record<string, string | number | boolean | undefined>) {
  const q = new URLSearchParams()
  for (const [k, v] of Object.entries(params || {})) {
    if (v !== undefined && v !== "" && v !== null) q.set(k, String(v))
  }
  return q.toString()
}

export type Paged<T> = T & { total: number; limit: number; offset: number }

export const admin = {
  stats: () => get<Stats>("/admin/stats"),

  // sort/order are server-side ordering (plan §70): products accept
  // created_at, name, slug, type, updated_at; an unknown key is
  // ignored by the server and keeps the default (created_at desc).
  listProducts: (params?: {
    search?: string
    type?: string
    sort?: string
    order?: "asc" | "desc"
    limit?: number
    offset?: number
  }) => get<Paged<{ products: Product[] }>>(`/admin/products?${listQuery(params)}`),
  getProduct: (id: string) => get<Product>(`/admin/products/${id}`),
  createProduct: (data: {
    name: string
    slug: string
    type: string
    feed_license_required?: boolean
    download_url?: string
  }) => post<Product>("/admin/products", data),
  updateProduct: (id: string, data: Partial<Product>) => put<Product>(`/admin/products/${id}`, data),
  deleteProduct: (id: string) => del(`/admin/products/${id}`),

  listPlans: (params?: { product_id?: string; search?: string; limit?: number; offset?: number }) =>
    get<Paged<{ plans: Plan[] }>>(`/admin/plans?${listQuery(params)}`),
  getPlan: (id: string) => get<Plan>(`/admin/plans/${id}`),
  createPlan: (data: Partial<Plan>) => post<Plan>("/admin/plans", data),
  updatePlan: (id: string, data: Partial<Plan>) => put<Plan>(`/admin/plans/${id}`, data),
  deletePlan: (id: string) => del(`/admin/plans/${id}`),

  createEntitlement: (data: { plan_id: string; feature: string; value_type: string; value: string }) =>
    post<Entitlement>("/admin/entitlements", data),
  updateEntitlement: (id: string, data: Partial<Entitlement>) => put<Entitlement>(`/admin/entitlements/${id}`, data),
  deleteEntitlement: (id: string) => del(`/admin/entitlements/${id}`),

  listLicenses: (params?: {
    product_id?: string
    status?: string
    search?: string
    external_customer_id?: string
    external_workspace_id?: string
    // Server-side ordering. The API refuses a column it does not
    // know rather than quietly serving a different order, so these
    // have to match its allowlist: created_at, valid_until, email,
    // status, product, plan.
    sort?: string
    order?: "asc" | "desc"
    offset?: number
    limit?: number
  }) => {
    const q = new URLSearchParams()
    if (params?.product_id) q.set("product_id", params.product_id)
    if (params?.status) q.set("status", params.status)
    if (params?.search) q.set("search", params.search)
    if (params?.external_customer_id) q.set("external_customer_id", params.external_customer_id)
    if (params?.external_workspace_id) q.set("external_workspace_id", params.external_workspace_id)
    if (params?.sort) q.set("sort", params.sort)
    if (params?.order) q.set("order", params.order)
    if (params?.offset) q.set("offset", String(params.offset))
    if (params?.limit) q.set("limit", String(params.limit))
    return get<{
      licenses: License[]
      total: number
      limit: number
      offset: number
      license_key_hints: Record<string, string>
    }>(`/admin/licenses?${q}`)
  },
  // The licence keeps its own shape; only the key is gone, replaced by
  // a last-four hint.
  getLicense: (id: string) =>
    get<License & { license_key_hint: string; key_usable: boolean; key_unusable_reason?: string }>(
      `/admin/licenses/${id}`,
    ),
  // The key is never in a list or detail payload — one explicit
  // request per key, audited server-side.
  revealLicenseKey: (id: string) => get<{ license_key: string }>(`/admin/licenses/${id}/key`),
  // Re-queues the "here is your key" mail. The address is the one on
  // the licence; the server does not accept one from here.
  resendLicenseEmail: (id: string) =>
    post<{ queued: boolean; email: string }>(`/admin/licenses/${id}/resend-email`, {}),
  createLicense: (data: {
    product_id: string
    plan_id: string
    email: string
    notes?: string
    external_customer_id?: string
    external_workspace_id?: string
    valid_until?: string
  }) => post<License>("/admin/licenses", data),
  // Empty valid_until clears the expiry (perpetual license).
  setLicenseValidUntil: (id: string, validUntil: string) =>
    post<License>(`/admin/licenses/${id}/valid-until`, { valid_until: validUntil }),
  // Empty updates_until means updates for life.
  setLicenseUpdatesUntil: (id: string, updatesUntil: string) =>
    post<License>(`/admin/licenses/${id}/updates-until`, { updates_until: updatesUntil }),
  revokeLicense: (id: string) => post(`/admin/licenses/${id}/revoke`),
  suspendLicense: (id: string) => post(`/admin/licenses/${id}/suspend`),
  reinstateLicense: (id: string) => post(`/admin/licenses/${id}/reinstate`),
  refundLicense: (id: string) => post(`/admin/licenses/${id}/refund`),

  deleteActivation: (id: string) => del(`/admin/activations/${id}`),

  listAPIKeys: (params?: { product_id?: string; search?: string; limit?: number; offset?: number }) =>
    get<Paged<{ api_keys: APIKey[] }>>(`/admin/api-keys?${listQuery(params)}`),
  createAPIKey: (data: { product_id?: string; name: string; scopes?: string[] }) =>
    post<APIKey & { key: string }>("/admin/api-keys", data),
  rotateAPIKey: (id: string) => post<APIKey & { key: string }>(`/admin/api-keys/${id}/rotate`, {}),
  deleteAPIKey: (id: string) => del(`/admin/api-keys/${id}`),

  listWebhooks: (params?: { product_id?: string; search?: string; limit?: number; offset?: number }) =>
    get<Paged<{ webhooks: WebhookConfig[] }>>(`/admin/webhooks?${listQuery(params)}`),
  createWebhook: (data: { product_id: string; url: string; events: string[] }) =>
    post<WebhookConfig & { secret: string }>("/admin/webhooks", data),
  updateWebhook: (id: string, data: Partial<WebhookConfig>) => put<WebhookConfig>(`/admin/webhooks/${id}`, data),
  deleteWebhook: (id: string) => del(`/admin/webhooks/${id}`),
  listWebhookDeliveries: (
    id: string,
    params?: { offset?: number; limit?: number; status?: string; event?: string },
  ) => {
    const q = new URLSearchParams()
    if (params?.offset) q.set("offset", String(params.offset))
    if (params?.limit) q.set("limit", String(params.limit))
    if (params?.status) q.set("status", params.status)
    if (params?.event) q.set("event", params.event)
    return get<{ deliveries: WebhookDeliveryLog[]; total: number }>(`/admin/webhooks/${id}/deliveries?${q}`)
  },
  resendWebhookDelivery: (webhookId: string, deliveryId: string) =>
    post<WebhookDeliveryLog>(`/admin/webhooks/${webhookId}/deliveries/${deliveryId}/resend`),
  testWebhook: (id: string) =>
    post<{ status: string; delivery_id: string; response_code: number; response_body: string }>(
      `/admin/webhooks/${id}/test`,
    ),

  getLicenseUsage: (id: string, params?: { feature?: string; offset?: number; limit?: number }) => {
    const q = new URLSearchParams()
    if (params?.feature) q.set("feature", params.feature)
    if (params?.offset) q.set("offset", String(params.offset))
    if (params?.limit) q.set("limit", String(params.limit))
    return get<{ events: UsageEvent[]; counters: UsageCounter[]; total: number }>(`/admin/licenses/${id}/usage?${q}`)
  },
  resetUsageCounter: (id: string, data: { feature: string; period?: string; period_key?: string }) =>
    post(`/admin/licenses/${id}/usage/reset`, data),

  getLicenseSeats: (id: string, params?: { limit?: number; offset?: number }) =>
    get<Paged<{ seats: Seat[]; active_count: number }>>(`/admin/licenses/${id}/seats?${listQuery(params)}`),

  getAnalytics: (params?: { product_id?: string; from?: string; to?: string; granularity?: string }) => {
    const q = new URLSearchParams()
    if (params?.product_id) q.set("product_id", params.product_id)
    if (params?.from) q.set("from", params.from)
    if (params?.to) q.set("to", params.to)
    if (params?.granularity) q.set("granularity", params.granularity)
    return get<{ snapshots: AnalyticsSnapshot[] | AggregatedSnapshot[] }>(`/admin/analytics?${q}`)
  },

  getAnalyticsSummary: (params?: {
    product_id?: string
    plan_id?: string
    license_type?: string
    status?: string
    from?: string
    to?: string
  }) => {
    const q = new URLSearchParams()
    if (params?.product_id) q.set("product_id", params.product_id)
    if (params?.plan_id) q.set("plan_id", params.plan_id)
    if (params?.license_type) q.set("license_type", params.license_type)
    if (params?.status) q.set("status", params.status)
    if (params?.from) q.set("from", params.from)
    if (params?.to) q.set("to", params.to)
    return get<AnalyticsSummary>(`/admin/analytics/summary?${q}`)
  },

  getAnalyticsBreakdown: (params: {
    product_id?: string
    plan_id?: string
    license_type?: string
    status?: string
    from?: string
    to?: string
    dimension: string
  }) => {
    const q = new URLSearchParams()
    if (params.product_id) q.set("product_id", params.product_id)
    if (params.plan_id) q.set("plan_id", params.plan_id)
    if (params.license_type) q.set("license_type", params.license_type)
    if (params.status) q.set("status", params.status)
    if (params.from) q.set("from", params.from)
    if (params.to) q.set("to", params.to)
    q.set("dimension", params.dimension)
    return get<{ items: BreakdownItem[] }>(`/admin/analytics/breakdown?${q}`)
  },

  getAnalyticsUsageTop: (params?: { product_id?: string; from?: string; to?: string; limit?: number }) => {
    const q = new URLSearchParams()
    if (params?.product_id) q.set("product_id", params.product_id)
    if (params?.from) q.set("from", params.from)
    if (params?.to) q.set("to", params.to)
    if (params?.limit) q.set("limit", String(params.limit))
    return get<{ features: FeatureUsageItem[] }>(`/admin/analytics/usage-top?${q}`)
  },

  getAnalyticsActivationTrend: (params?: { product_id?: string; from?: string; to?: string }) => {
    const q = new URLSearchParams()
    if (params?.product_id) q.set("product_id", params.product_id)
    if (params?.from) q.set("from", params.from)
    if (params?.to) q.set("to", params.to)
    return get<{ trend: TrendPoint[] }>(`/admin/analytics/activation-trend?${q}`)
  },

  // Cuts a licence loose from a Stripe subscription that is over, so
  // it can be managed locally again. Refused (409) while Stripe still
  // has the subscription.
  unlinkStripeSubscription: (id: string) => post<{ status: string }>(`/admin/licenses/${id}/stripe/unlink`, {}),
  changeLicensePlan: (id: string, data: { plan_id: string }) => post(`/admin/licenses/${id}/change-plan`, data),

  // Addons
  listAddons: (params?: { product_id?: string; search?: string; limit?: number; offset?: number }) =>
    get<Paged<{ addons: Addon[] }>>(`/admin/addons?${listQuery(params)}`),
  createAddon: (data: Partial<Addon>) => post<Addon>("/admin/addons", data),
  updateAddon: (id: string, data: Partial<Addon>) => put<Addon>(`/admin/addons/${id}`, data),
  deleteAddon: (id: string) => del(`/admin/addons/${id}`),
  getLicenseAddons: (id: string, params?: { limit?: number; offset?: number }) =>
    get<Paged<{ addons: LicenseAddon[] }>>(`/admin/licenses/${id}/addons?${listQuery(params)}`),
  addLicenseAddon: (id: string, addonId: string) =>
    post<LicenseAddon>(`/admin/licenses/${id}/addons`, { addon_id: addonId }),
  removeLicenseAddon: (id: string, addonId: string) => del(`/admin/licenses/${id}/addons/${addonId}`),
  getFloatingSessions: (id: string, params?: { limit?: number; offset?: number }) =>
    get<Paged<{ sessions: FloatingSession[]; active: number }>>(`/admin/licenses/${id}/floating?${listQuery(params)}`),

  // sort ∈ {created_at, entity, action, actor}; unknown keys keep the
  // default order (created_at desc).
  listAuditLogs: (params?: {
    entity?: string
    entity_id?: string
    product_id?: string
    sort?: string
    order?: "asc" | "desc"
    offset?: number
    limit?: number
  }) => {
    const q = new URLSearchParams()
    if (params?.entity) q.set("entity", params.entity)
    if (params?.entity_id) q.set("entity_id", params.entity_id)
    if (params?.product_id) q.set("product_id", params.product_id)
    if (params?.sort) q.set("sort", params.sort)
    if (params?.order) q.set("order", params.order)
    if (params?.offset) q.set("offset", String(params.offset))
    if (params?.limit) q.set("limit", String(params.limit))
    return get<{ audit_logs: AuditLog[]; total: number }>(`/admin/audit-logs?${q}`)
  },

  // sort ∈ {created_at, email, name}; unknown keys keep the default
  // order (created_at desc).
  listUsers: (params?: { search?: string; sort?: string; order?: "asc" | "desc"; offset?: number; limit?: number }) => {
    const q = new URLSearchParams()
    if (params?.search) q.set("search", params.search)
    if (params?.sort) q.set("sort", params.sort)
    if (params?.order) q.set("order", params.order)
    if (params?.offset) q.set("offset", String(params.offset))
    if (params?.limit) q.set("limit", String(params.limit))
    return get<{ users: User[]; total: number }>(`/admin/users?${q}`)
  },

  // Team (admin management)
  listTeam: (params?: { limit?: number; offset?: number }) =>
    get<Paged<{ members: User[] }>>(`/admin/team?${listQuery(params)}`),
  inviteTeamMember: (data: { email: string; role?: string }) => post<User>("/admin/team", data),
  removeTeamMember: (id: string) => del(`/admin/team/${id}`),

  getAnalyticsInsights: (params?: { product_id?: string }) => {
    const q = new URLSearchParams()
    if (params?.product_id) q.set("product_id", params.product_id)
    return get<AnalyticsInsights>(`/admin/analytics/insights?${q}`)
  },

  getUserDetail: (id: string) => get<UserDetail & { license_key_hints: Record<string, string> }>(`/admin/users/${id}`),

  // Settings
  getSettings: () =>
    get<{
      settings: Record<string, string>
      secrets_set?: Record<string, boolean>
      email?: { configured: boolean; provider: string; source: string; host: string; from: string }
    }>("/admin/settings"),
  updateSettings: (settings: Record<string, string>) => put<{ status: string }>("/admin/settings", { settings }),
  sendTestEmail: (to?: string) => post<{ status: string }>("/admin/settings/test-email", to ? { to } : {}),
  clearSecretSetting: (key: string) => del<{ status: string; key: string }>(`/admin/settings/secrets/${key}`),

  // Email Templates
  getEmailTemplates: () =>
    get<{ templates: Record<string, { custom: string; default: string }> }>("/admin/email-templates"),

  // System
  getVersion: () => get<{ version: string; commit: string; build_date: string }>("/version"),
  checkUpdate: () =>
    get<{
      available: boolean
      latest_version: string
      current_version: string
      release_url?: string
      release_date?: string
      changelog?: string
      update_command?: string
      checked_at?: string
    }>("/admin/system/update-check"),

  // ─── Releases (industry-standard bundle model) ───
  // sort ∈ {created_at, version, published_at, status, channel}.
  listReleases: (params?: {
    product_id?: string
    channel?: string
    status?: string
    sort?: string
    order?: "asc" | "desc"
    limit?: number
    offset?: number
  }) => {
    const q = new URLSearchParams()
    if (params?.product_id) q.set("product_id", params.product_id)
    if (params?.channel) q.set("channel", params.channel)
    if (params?.status) q.set("status", params.status)
    if (params?.sort) q.set("sort", params.sort)
    if (params?.order) q.set("order", params.order)
    if (params?.limit) q.set("limit", String(params.limit))
    if (params?.offset) q.set("offset", String(params.offset))
    return get<{ releases: Release[]; total: number; limit: number; offset: number }>(`/admin/releases?${q}`)
  },
  getRelease: (id: string) => get<Release>(`/admin/releases/${id}`),
  createRelease: (data: {
    product_id: string
    version: string
    channel?: string
    name?: string
    release_notes?: string
  }) => post<Release>("/admin/releases", data),
  addArtifact: (
    releaseId: string,
    data: {
      platform: string
      content_type?: string
      expected_size?: number
      filename?: string
    },
  ) =>
    post<{ artifact: ReleaseArtifact; upload_url: string; expires_at: string }>(
      `/admin/releases/${releaseId}/artifacts`,
      data,
    ),
  finalizeArtifact: (releaseId: string, artifactId: string, data: { expected_sha256?: string }) =>
    post<ReleaseArtifact>(`/admin/releases/${releaseId}/artifacts/${artifactId}/finalize`, data),
  deleteArtifact: (releaseId: string, artifactId: string) =>
    del(`/admin/releases/${releaseId}/artifacts/${artifactId}`),
  publishRelease: (id: string) => post<Release>(`/admin/releases/${id}/actions/publish`),
  yankRelease: (id: string, reason: string) => post<Release>(`/admin/releases/${id}/actions/yank`, { reason }),
  unyankRelease: (id: string) => post<Release>(`/admin/releases/${id}/actions/unyank`),
  updateReleaseNotes: (id: string, data: { name?: string; release_notes?: string }) =>
    patch<Release>(`/admin/releases/${id}`, data),
  deleteRelease: (id: string) => del(`/admin/releases/${id}`),

  // ─── Release signing keys (per product) ───
  listSigningKeys: (productId: string) =>
    get<{ keys: ReleaseSigningKey[] }>(`/admin/products/${productId}/signing-keys`),
  generateSigningKey: (productId: string) => post<ReleaseSigningKey>(`/admin/products/${productId}/signing-key`),
  rotateSigningKey: (productId: string, note: string) =>
    post<ReleaseSigningKey>(`/admin/products/${productId}/signing-key/rotate`, { note }),
  deactivateSigningKey: (productId: string, note: string) =>
    request<{ status: string }>(`/admin/products/${productId}/signing-key`, {
      method: "DELETE",
      body: JSON.stringify({ note }),
    }),
  publicKeyURL: (productId: string) => `${BASE}/admin/products/${productId}/signing-key/public.pem`,
  tauriPublicKey: (productId: string) =>
    get<{ pubkey: string }>(`/admin/products/${productId}/signing-key/tauri-pubkey`),

  // ─── Commerce: coupons, tax rates, orders (Phase 4) ───
  // sort ∈ {created_at, code, type, expires_at, redemptions, active}.
  listCoupons: (params?: { search?: string; sort?: string; order?: "asc" | "desc"; limit?: number; offset?: number }) =>
    get<Paged<{ coupons: Coupon[] }>>(`/admin/coupons?${listQuery(params)}`),
  getCoupon: (id: string) => get<Coupon>(`/admin/coupons/${id}`),
  createCoupon: (data: CouponInput) => post<Coupon>("/admin/coupons", data),
  // The coupon update route is PUT, not PATCH: the handler reads a
  // partial body either way, but the verb on the wire is the one the
  // router registered. times_redeemed is deliberately absent — the
  // counter moves only on redemption.
  updateCoupon: (id: string, data: CouponInput) => put<Coupon>(`/admin/coupons/${id}`, data),
  deleteCoupon: (id: string) => del(`/admin/coupons/${id}`),

  listTaxRates: (params?: { search?: string; limit?: number; offset?: number }) =>
    get<Paged<{ tax_rates: TaxRate[] }>>(`/admin/tax-rates?${listQuery(params)}`),
  getTaxRate: (id: string) => get<TaxRate>(`/admin/tax-rates/${id}`),
  createTaxRate: (data: TaxRateInput) => post<TaxRate>("/admin/tax-rates", data),
  updateTaxRate: (id: string, data: Partial<TaxRateInput>) => patch<TaxRate>(`/admin/tax-rates/${id}`, data),
  deleteTaxRate: (id: string) => del(`/admin/tax-rates/${id}`),

  // sort ∈ {created_at, order_number, customer, status, total,
  // paid_at, updated_at}.
  listOrders: (params?: {
    search?: string
    status?: string
    sort?: string
    order?: "asc" | "desc"
    limit?: number
    offset?: number
  }) => get<Paged<{ orders: Order[] }>>(`/admin/orders?${listQuery(params)}`),
  getOrder: (id: string) => get<{ order: Order; invoices: OrderInvoice[] }>(`/admin/orders/${id}`),
  // Refund takes no body: the server stamps the refund time itself.
  refundOrder: (id: string) => post<Order>(`/admin/orders/${id}/refund`),
  // sort ∈ {created_at, invoice_number, status, total, paid_at,
  // due_at}; the invoice list's natural order is created_at ASC
  // (oldest first) unlike the other ledgers.
  listOrderInvoices: (
    id: string,
    params?: { sort?: string; order?: "asc" | "desc"; limit?: number; offset?: number },
  ) => get<Paged<{ invoices: OrderInvoice[] }>>(`/admin/orders/${id}/invoices?${listQuery(params)}`),
  // The order's refund ledger (plan §79), one row per refund issued.
  // sort ∈ {created_at, amount, status, provider}, default created_at
  // desc. The rows carry their own shape, not the order's.
  listOrderRefunds: (id: string, params?: { sort?: string; order?: "asc" | "desc" }) =>
    get<{ refunds: Refund[] }>(`/admin/orders/${id}/refunds?${listQuery(params)}`),
  // Price preview without persistence — registered as /admin/quotes,
  // outside /orders/:id so it cannot collide with that param route.
  quoteOrder: (data: QuoteRequest) => post<QuoteResult>("/admin/quotes", data),

  // ─── Categories (marketplace catalog) ───
  // Discovery facets only — no price, entitlement or license meaning.
  // Deleting a category detaches its products (the join cascades), it
  // never blocks on attached products. Update is PATCH (partial body);
  // create derives a slug from the name when one is not sent.
  listCategories: (params?: { search?: string; limit?: number; offset?: number }) =>
    get<Paged<{ categories: Category[] }>>(`/admin/categories?${listQuery(params)}`),
  getCategory: (id: string) => get<Category>(`/admin/categories/${id}`),
  createCategory: (data: CategoryInput) => post<Category>("/admin/categories", data),
  updateCategory: (id: string, data: Partial<CategoryInput>) => patch<Category>(`/admin/categories/${id}`, data),
  deleteCategory: (id: string) => del(`/admin/categories/${id}`),

  // ─── Resellers (partner accounts + commission ledger + wholesale prices) ───
  // Money is integer minor units and rates are integer basis points —
  // never float. Commission accrual is idempotent per (reseller,
  // order): a retried POST answers the original row, never a second
  // payout. Delete refuses (409) while the reseller still owns
  // licences (RESSELLER_HAS_ALLOCATIONS) or has commission rows
  // (RESSELLER_HAS_COMMISSIONS).
  listResellers: (params?: { search?: string; status?: string; limit?: number; offset?: number }) =>
    get<Paged<{ resellers: Reseller[] }>>(`/admin/resellers?${listQuery(params)}`),
  getReseller: (id: string) => get<{ reseller: Reseller; license_count: number }>(`/admin/resellers/${id}`),
  createReseller: (data: {
    name: string
    contact_email: string
    status?: string
    commission_bps?: number
    notes?: string
  }) => post<Reseller>("/admin/resellers", data),
  updateReseller: (
    id: string,
    data: { name?: string; contact_email?: string; status?: string; commission_bps?: number; notes?: string },
  ) => patch<Reseller>(`/admin/resellers/${id}`, data),
  deleteReseller: (id: string) => del<void>(`/admin/resellers/${id}`),
  // A licence belongs to at most one reseller: a second claim is 409
  // ALREADY_ALLOCATED, and a licence_id naming nothing is 404
  // LICENSE_NOT_FOUND.
  allocateResellerLicense: (id: string, licenseId: string) =>
    post<{ reseller_id: string; license_id: string }>(`/admin/resellers/${id}/licenses`, { license_id: licenseId }),
  listResellerLicenses: (id: string, params?: { limit?: number; offset?: number }) =>
    get<Paged<{ licenses: License[] }>>(`/admin/resellers/${id}/licenses?${listQuery(params)}`),
  deallocateResellerLicense: (id: string, licenseId: string) =>
    del<void>(`/admin/resellers/${id}/licenses/${encodeURIComponent(licenseId)}`),
  // Commission ledger. Status filter: accrued|approved|paid|cancelled.
  listCommissions: (id: string, params?: { status?: string; limit?: number; offset?: number }) =>
    get<Paged<{ commissions: Commission[] }>>(`/admin/resellers/${id}/commissions?${listQuery(params)}`),
  // bps is optional and defaults to the reseller's contract rate. A
  // repeat for the same order answers the ORIGINAL row (200, not 201).
  accrueCommission: (id: string, data: { order_id: string; basis_minor: number; bps?: number }) =>
    post<Commission>(`/admin/resellers/${id}/commissions`, data),
  // Body is optional ({paid_at?} RFC 3339); omitted means "now".
  markCommissionPaid: (id: string, commissionId: string) =>
    post<Commission>(`/admin/resellers/${id}/commissions/${commissionId}/paid`),
  // Wholesale price overrides — the whole list is unpaged.
  listPriceOverrides: (id: string) => get<Paged<{ prices: ResellerPriceOverride[] }>>(`/admin/resellers/${id}/prices`),
  // PUT semantics: the (reseller, plan) pair is the whole identity.
  setPriceOverride: (id: string, planId: string, data: { unit_amount_minor: number; currency: string }) =>
    put<ResellerPriceOverride>(`/admin/resellers/${id}/prices/${encodeURIComponent(planId)}`, data),
  deletePriceOverride: (id: string, planId: string) =>
    del<void>(`/admin/resellers/${id}/prices/${encodeURIComponent(planId)}`),

  // ─── Affiliates (referral program) ───
  // Delete refuses (409) while conversions (AFFILIATE_HAS_CONVERSIONS)
  // or payouts (AFFILIATE_HAS_PAYOUTS) exist — suspend instead.
  listAffiliates: (params?: { search?: string; status?: string; limit?: number; offset?: number }) =>
    get<Paged<{ affiliates: Affiliate[] }>>(`/admin/affiliates?${listQuery(params)}`),
  getAffiliate: (id: string) =>
    get<{ affiliate: Affiliate; pending_commission_minor: number }>(`/admin/affiliates/${id}`),
  createAffiliate: (data: {
    name: string
    contact_email: string
    status?: string
    commission_model?: string
    commission_bps?: number
    commission_minor?: number
    payout_method?: string
    notes?: string
  }) => post<Affiliate>("/admin/affiliates", data),
  updateAffiliate: (
    id: string,
    data: {
      name?: string
      contact_email?: string
      status?: string
      commission_model?: string
      commission_bps?: number
      commission_minor?: number
      payout_method?: string
      notes?: string
    },
  ) => patch<Affiliate>(`/admin/affiliates/${id}`, data),
  deleteAffiliate: (id: string) => del<void>(`/admin/affiliates/${id}`),
  // Referral codes. The handle is immutable after creation; a delete
  // refuses (409 REFERRAL_CODE_HAS_CONVERSIONS) while conversions
  // point at it — deactivate instead.
  listReferralCodes: (id: string, params?: { limit?: number; offset?: number }) =>
    get<Paged<{ codes: ReferralCode[] }>>(`/admin/affiliates/${id}/codes?${listQuery(params)}`),
  createReferralCode: (id: string, data: { code: string; landing_url?: string; active?: boolean }) =>
    post<ReferralCode>(`/admin/affiliates/${id}/codes`, data),
  updateReferralCode: (id: string, codeId: string, data: { landing_url?: string; active?: boolean }) =>
    patch<ReferralCode>(`/admin/affiliates/${id}/codes/${encodeURIComponent(codeId)}`, data),
  deleteReferralCode: (id: string, codeId: string) =>
    del<void>(`/admin/affiliates/${id}/codes/${encodeURIComponent(codeId)}`),
  // Conversions. Status filter: pending|approved|paid|rejected|reversed.
  // The three review decisions live outside the affiliate scope
  // (POST /admin/conversions/:id/…) and share the state machine:
  // illegal moves are 409 CONVERSION_TRANSITION_INVALID, and a
  // conversion a requested payout has claimed is frozen (409
  // CONVERSION_IN_PAYOUT — fail that payout first).
  listAffiliateConversions: (id: string, params?: { status?: string; limit?: number; offset?: number }) =>
    get<Paged<{ conversions: AffiliateConversion[] }>>(`/admin/affiliates/${id}/conversions?${listQuery(params)}`),
  approveConversion: (conversionId: string) =>
    post<AffiliateConversion>(`/admin/conversions/${encodeURIComponent(conversionId)}/approve`),
  rejectConversion: (conversionId: string) =>
    post<AffiliateConversion>(`/admin/conversions/${encodeURIComponent(conversionId)}/reject`),
  reverseConversion: (conversionId: string) =>
    post<AffiliateConversion>(`/admin/conversions/${encodeURIComponent(conversionId)}/reverse`),
  // Payouts. Create claims whole conversions oldest-first (a repeat
  // for the same order is never double-paid). amount_minor is an
  // upper bound — omitted or 0 settles the whole accrued balance.
  // Only a requested payout can move (409 PAYOUT_TRANSITION_INVALID).
  listAffiliatePayouts: (id: string, params?: { limit?: number; offset?: number }) =>
    get<Paged<{ payouts: AffiliatePayout[] }>>(`/admin/affiliates/${id}/payouts?${listQuery(params)}`),
  createAffiliatePayout: (id: string, data: { amount_minor?: number; notes?: string }) =>
    post<AffiliatePayout>(`/admin/affiliates/${id}/payouts`, data),
  markPayoutPaid: (payoutId: string) => post<AffiliatePayout>(`/admin/payouts/${encodeURIComponent(payoutId)}/paid`),
  markPayoutFailed: (payoutId: string, notes: string) =>
    post<AffiliatePayout>(`/admin/payouts/${encodeURIComponent(payoutId)}/failed`, { notes }),

  // ─── Order billing block + invoice state transitions ───
  // PATCH merge semantics (see OrderBillingPatch). Invoice moves:
  // a paid invoice is never voidable (409 INVOICE_NOT_VOIDABLE —
  // refund the order first) and any other illegal move is 409
  // INVOICE_TRANSITION_INVALID. Both answer the updated invoice.
  updateOrderBilling: (id: string, data: OrderBillingPatch) => patch<Order>(`/admin/orders/${id}/billing`, data),
  voidOrderInvoice: (orderId: string, invoiceId: string) =>
    post<OrderInvoice>(`/admin/orders/${orderId}/invoices/${encodeURIComponent(invoiceId)}/void`),
  markOrderInvoiceUncollectible: (orderId: string, invoiceId: string) =>
    post<OrderInvoice>(`/admin/orders/${orderId}/invoices/${encodeURIComponent(invoiceId)}/mark-uncollectible`),

  // ─── Review moderation (marketplace) ───
  // The state machine and nothing else moves: pending → approved |
  // rejected, approved → rejected (unpublish), rejected → approved
  // (republish). An illegal move answers 409
  // REVIEW_TRANSITION_INVALID. reply with "" clears the public answer.
  listReviews: (params?: { product_id?: string; status?: string; limit?: number; offset?: number }) =>
    get<Paged<{ reviews: Review[] }>>(`/admin/reviews?${listQuery(params)}`),
  getReview: (id: string) => get<Review>(`/admin/reviews/${encodeURIComponent(id)}`),
  approveReview: (id: string) => post<Review>(`/admin/reviews/${encodeURIComponent(id)}/approve`),
  rejectReview: (id: string) => post<Review>(`/admin/reviews/${encodeURIComponent(id)}/reject`),
  replyReview: (id: string, adminReply: string) =>
    put<Review>(`/admin/reviews/${encodeURIComponent(id)}/reply`, { admin_reply: adminReply }),
  deleteReview: (id: string) => del<void>(`/admin/reviews/${encodeURIComponent(id)}`),

  // ─── Reports (plan §43) ───
  // The reporting engine. The catalog names the report types and
  // groupings the server serves; one run answers {series, totals}.
  // Series rows carry `period` for time series — the resellers and
  // affiliates reports answer with a ranked top list instead (no
  // period). Metric keys are self-describing: *_count is an integer
  // count, *_minor is integer minor-unit money (see lib/money).
  listReportTypes: () => get<ReportCatalog>("/admin/reports"),
  getReport: (type: string, params?: { from?: string; to?: string; group_by?: string }) =>
    get<ReportData>(`/admin/reports/${encodeURIComponent(type)}?${listQuery(params)}`),
  // Export is a file download, not a JSON envelope: fetch the body as
  // a blob so the CSV keeps its exact bytes. Failures still surface as
  // ApiError, so the §90 toasts behave like everywhere else.
  exportReport: async (
    type: string,
    params: { from?: string; to?: string; group_by?: string; format: "csv" | "json" },
  ) => {
    const res = await fetch(`${BASE}/admin/reports/${encodeURIComponent(type)}/export?${listQuery(params)}`, {
      credentials: "include",
    })
    if (!res.ok) {
      const raw = await res.text()
      let message = `Export failed (${res.status})`
      let code = ""
      try {
        const json = JSON.parse(raw)
        message = json?.error?.message || message
        code = typeof json?.error?.code === "string" ? json.error.code : ""
      } catch {
        // non-JSON error body — keep the generic message
      }
      throw new ApiError(message, res.status, code, res.headers.get("X-Request-ID") || "")
    }
    return res.blob()
  },

  // ─── RBAC: custom roles & permissions (plan §8) ───
  // System bundles (is_system) refuse rename / re-permission / delete
  // with 409 SYSTEM_ROLE_PROTECTED; a role still held by users refuses
  // delete with 409 ROLE_IN_USE; a name already taken — in any
  // spelling — is 409 DUPLICATE. Assign / revoke are idempotent.
  listPermissions: () => get<{ permissions: string[] }>("/admin/rbac/permissions"),
  listRoles: (params?: { search?: string; limit?: number; offset?: number }) =>
    get<Paged<{ roles: Role[] }>>(`/admin/rbac/roles?${listQuery(params)}`),
  getRole: (id: string) => get<Role>(`/admin/rbac/roles/${encodeURIComponent(id)}`),
  createRole: (data: { name: string; description?: string; permissions?: string[] }) =>
    post<Role>("/admin/rbac/roles", data),
  updateRole: (id: string, data: { name?: string; description?: string; permissions?: string[] }) =>
    patch<Role>(`/admin/rbac/roles/${encodeURIComponent(id)}`, data),
  deleteRole: (id: string) => del<void>(`/admin/rbac/roles/${encodeURIComponent(id)}`),
  setRolePermissions: (id: string, permissions: string[]) =>
    put<Role>(`/admin/rbac/roles/${encodeURIComponent(id)}/permissions`, { permissions }),
  listUserRoles: (userId: string) =>
    get<{ roles: Role[]; total: number }>(`/admin/rbac/users/${encodeURIComponent(userId)}/roles`),
  assignUserRole: (userId: string, roleId: string) =>
    post<{ user_id: string; role_id: string }>(`/admin/rbac/users/${encodeURIComponent(userId)}/roles`, {
      role_id: roleId,
    }),
  // Revoke reads role_id from a JSON body on DELETE — the one delete
  // in this client that carries a body.
  revokeUserRole: (userId: string, roleId: string) =>
    request<void>(`/admin/rbac/users/${encodeURIComponent(userId)}/roles`, {
      method: "DELETE",
      body: JSON.stringify({ role_id: roleId }),
    }),

  // ─── Global search (plan §86) — the command palette's data half ───
  // One lookup across the catalog, commerce and partner tables.
  // `types` is a comma-separated subset of the closed vocabulary;
  // absent means every type. The server clamps limit to 50 and
  // answers {results: [{type, id, title, subtitle, url}]}; url is an
  // SPA route. A licence is found by its key HASH server-side and the
  // hit never carries key material.
  search: (params: { q: string; types?: string; limit?: number }) =>
    get<{ results: AdminSearchResult[] }>(`/admin/search?${listQuery(params)}`),

  // ─── Config-in-DB (Agent B catalog) ───
  // The runtime config catalog: categories of typed keys, secrets
  // write-only (value always empty, `set` says whether one is stored).
  getConfig: () => get<{ categories: ConfigCategory[] }>("/admin/config"),
  // Save sends ONLY changed keys — plain values under `values`, new
  // secret material under `secrets`. Keys needing a restart to take
  // effect come back as restarted_required (the keys the server wants
  // bounced).
  updateConfig: (body: { values?: Record<string, string>; secrets?: Record<string, string> }) =>
    put<{ updated: string[]; restarted_required: string[] }>("/admin/config", body),
  // Reset drops the stored value (and the secret) back to the
  // default / env fallback for one key.
  resetConfigKey: (key: string) => del<{ reset: string }>(`/admin/config/${encodeURIComponent(key)}`),

  // ─── Privacy (plan §82): admin-side export + anonymize ───
  // The same document / anonymization the user can run on their own
  // account, for any user, audit-logged (privacy.export /
  // privacy.anonymize) with the admin as actor.
  exportUser: (id: string) => get<AccountExport>(`/admin/users/${encodeURIComponent(id)}/export`),
  anonymizeUser: (id: string) => post<AccountAnonymizeResult>(`/admin/users/${encodeURIComponent(id)}/anonymize`),
}

// ─── Types ───

export interface User {
  id: string
  email: string
  name: string
  avatar_url?: string
  role: string // "owner" | "admin" | "user"
  created_at: string
  updated_at: string
}

export interface Product {
  id: string
  name: string
  slug: string
  type: string
  minimum_supported_version?: string
  minimum_supported_message?: string
  require_signing: boolean
  // Update feeds answer only with a license key (maintenance-period products).
  feed_license_required?: boolean
  feed_gated_at?: string
  /** Vendor's download page, used as {{.DownloadURL}} in emails. */
  download_url?: string
  created_at: string
}

export interface Plan {
  id: string
  product_id: string
  name: string
  slug: string
  checkout_id: string
  license_type: string
  billing_interval?: string
  max_activations: number
  license_model?: string
  floating_timeout?: number
  token_ttl_days?: number
  max_seats: number
  trial_days: number
  grace_days: number
  stripe_price_id?: string
  // Maintenance period (perpetual plans): days of updates a purchase
  // includes (0 = for life) and the one-time renewal sold in the portal.
  updates_days?: number
  renewal_days?: number
  stripe_renewal_price_id?: string
  active: boolean
  sort_order: number
  created_at: string
  product?: Product
  entitlements?: Entitlement[]
}

export interface Entitlement {
  id: string
  plan_id: string
  feature: string
  value_type: string
  value: string
  quota_period?: string
  quota_unit?: string
}

export interface License {
  id: string
  product_id: string
  plan_id: string
  user_id?: string
  email: string
  // Present only where the server deliberately attaches it: the
  // customer portal (their own key) and the create-license response.
  // Admin list/detail carry a last-four hint instead — see PortalLicense.
  license_key?: string
  payment_provider?: string
  // Set only for subscription-backed Stripe licenses. A one-time
  // purchase has no subscription, so nothing to change, cancel, or
  // update a payment method for.
  stripe_subscription_id?: string
  status: string
  valid_from: string
  valid_until?: string
  // End of a perpetual license's update period; absent = no separate limit.
  updates_until?: string
  canceled_at?: string
  suspended_at?: string
  org_name?: string
  notes?: string
  external_customer_id?: string
  external_workspace_id?: string
  created_at: string
  updated_at: string
  product?: Product
  plan?: Plan
  // How many activations the licence has. Present on list rows, where
  // the activations themselves are not; the detail payload carries
  // both and they agree.
  activation_count?: number
  // Floating seats in use right now. A floating plan keeps occupancy
  // in its own table, so activation_count reads zero for one however
  // full it is; this is the number that answers "how full" there.
  active_session_count?: number
  activations?: Activation[]
  seats?: Seat[]
  addons?: LicenseAddon[]
}

// The portal always receives the key: it is the customer's own
// credential and also names the license in the activation and seat
// endpoints. Admin payloads never carry it.
export interface PortalLicense extends License {
  license_key: string
}

export interface Activation {
  id: string
  license_id: string
  identifier: string
  identifier_type: string
  label?: string
  ip_address?: string
  last_verified: string
  created_at: string
}

export interface APIKey {
  id: string
  product_id: string
  name: string
  prefix: string
  scopes: string[]
  last_used?: string
  last_used_ip?: string
  created_at: string
  product?: Product
}

export interface AuditLog {
  id: string
  entity: string
  entity_id: string
  action: string
  actor_id?: string
  actor_type?: string
  changes?: Record<string, unknown>
  ip_address?: string
  created_at: string
}

export interface Stats {
  total_licenses: number
  active_licenses: number
  total_activations: number
  total_products: number
  total_seats: number
  total_usage_events: number
  total_webhooks: number
  by_status: Record<string, number>
  recent_licenses: License[]
}

export interface Seat {
  id: string
  license_id: string
  email: string
  role: string
  user_id?: string
  invited_at: string
  accepted_at?: string
  removed_at?: string
  created_at: string
}

export interface UsageEvent {
  id: string
  license_id: string
  feature: string
  quantity: number
  metadata?: Record<string, unknown>
  ip_address?: string
  recorded_at: string
}

export interface UsageCounter {
  id: string
  license_id: string
  feature: string
  period: string
  period_key: string
  used: number
  updated_at: string
}

export interface WebhookConfig {
  id: string
  product_id: string
  url: string
  events: string[]
  active: boolean
  created_at: string
  updated_at: string
  product?: Product
}

// PortalWebhook is the customer's own webhook endpoint (account-scoped,
// not per-product like the admin WebhookConfig). The plaintext secret is
// returned exactly once on creation; after that only secret_prefix is
// ever seen.
export interface PortalWebhook {
  id: string
  url: string
  events: string[]
  active: boolean
  secret_prefix: string
  last_delivery_at?: string | null
  created_at: string
  updated_at: string
}

export interface WebhookDeliveryLog {
  id: string
  webhook_id: string
  event: string
  payload?: Record<string, unknown>
  response_code?: number
  response_body?: string
  status: string
  attempts: number
  created_at: string
  delivered_at?: string
}

export interface AnalyticsSnapshot {
  id: string
  date: string
  product_id: string
  total_licenses: number
  active_licenses: number
  new_licenses: number
  churned: number
  total_activations: number
  total_seats: number
  total_usage: number
}

export interface AnalyticsSummary {
  total_licenses: number
  active_licenses: number
  trialing_licenses: number
  expired_licenses: number
  canceled_licenses: number
  suspended_licenses: number
  revoked_licenses: number
  past_due_licenses: number
  total_activations: number
  total_seats: number
  avg_activations_per_license: number
}

export interface BreakdownItem {
  key: string
  label: string
  count: number
}

export interface FeatureUsageItem {
  feature: string
  total_usage: number
  unique_users: number
}

export interface TrendPoint {
  date: string
  count: number
}

export interface AggregatedSnapshot {
  period: string
  total_licenses: number
  active_licenses: number
  new_licenses: number
  churned: number
  total_activations: number
  total_seats: number
  total_usage: number
}

export interface FloatingSession {
  id: string
  license_id: string
  identifier: string
  label?: string
  ip_address?: string
  checked_out: string
  expires_at: string
  heartbeat: string
}

export interface Addon {
  id: string
  product_id: string
  name: string
  slug: string
  description?: string
  feature: string
  value_type: string
  value: string
  quota_period?: string
  quota_unit?: string
  active: boolean
  sort_order: number
  created_at: string
  product?: Product
}

export interface LicenseAddon {
  id: string
  license_id: string
  addon_id: string
  enabled: boolean
  created_at: string
  addon?: Addon
}

export interface MeteredBilling {
  id: string
  license_id: string
  feature: string
  quantity: number
  period_key: string
  synced: boolean
  created_at: string
}

export interface Subscription {
  id: string
  license_id: string
  user_id?: string
  plan_id: string
  status: string
  payment_provider?: string
  external_id?: string
  current_period_start?: string
  current_period_end?: string
  cancel_at_period_end: boolean
  canceled_at?: string
  trial_start?: string
  trial_end?: string
  metadata?: Record<string, unknown>
  created_at: string
  updated_at: string
  license?: License
  plan?: Plan
}

export interface GrowthMetrics {
  net_growth_rate: number
  trial_conversion: number
  avg_license_age_days: number
  median_license_age_days: number
  total_trials: number
  converted_trials: number
  new_last_30d: number
  churned_last_30d: number
}

export interface RetentionData {
  period: string
  start_count: number
  end_count: number
  retention_pct: number
  churn_pct: number
}

export interface LicenseAgeDistribution {
  bucket: string
  count: number
}

export interface RecentActivity {
  id: string
  entity: string
  entity_id: string
  action: string
  actor_type: string
  created_at: string
}

export interface TopUser {
  email: string
  user_id: string
  license_count: number
  active_count: number
  total_usage: number
  activation_count: number
}

export interface AnalyticsInsights {
  growth: GrowthMetrics
  age_distribution: LicenseAgeDistribution[]
  top_users: TopUser[]
  retention: RetentionData[]
  recent_activity: RecentActivity[]
}

export interface Invoice {
  id: string
  number: string
  status: string
  amount_due: number
  amount_paid: number
  currency: string
  created: number
  period_start: number
  period_end: number
  invoice_pdf: string
  hosted_url: string
}

export interface UserDetail {
  user: User
  licenses: License[]
  subscriptions: Subscription[]
  total_usage: number
  active_seats: number
  activations: number
  recent_audit_logs: AuditLog[]
}

// Release: a logical version event for a product. Contains many platform
// artifacts. Lifecycle status is on the release; yank affects all artifacts.
export interface Release {
  id: string
  product_id: string
  version: string
  channel: "stable" | "beta" | "alpha" | "dev"
  name: string
  release_notes: string
  status: "draft" | "published" | "yanked"
  yanked_reason?: string
  published_at?: string
  yanked_at?: string
  created_at: string
  updated_at: string
  product?: Product
  artifacts?: ReleaseArtifact[]
}

// ReleaseArtifact: per-platform binary inside a Release.
export interface ReleaseArtifact {
  id: string
  release_id: string
  platform: string
  /** Name the file had when uploaded; empty for older artifacts. */
  filename?: string
  file_key: string
  file_size: number
  sha256: string
  ed25519_sig: string
  content_type: string
  signing_key_id?: string
  created_at: string
  updated_at: string
}

export interface ReleaseSigningKey {
  id: string
  product_id: string
  public_key: string
  active: boolean
  note?: string
  created_at: string
  rotated_at?: string
}

export const RELEASE_PLATFORMS = [
  "darwin-arm64",
  "darwin-x64",
  "windows-arm64",
  "windows-x64",
  "linux-arm64",
  "linux-x64",
  "linux-armhf",
] as const

export const RELEASE_CHANNELS = ["stable", "beta", "alpha", "dev"] as const

// ─── Commerce (Phase 4): coupons, tax rates, orders ───
//
// Money is integer minor units and percents are integer basis points
// (10000 = 100%) end to end — see lib/money.ts for display and input.

export interface Coupon {
  id: string
  code: string
  // The stored vocabulary is "percent_off" | "fixed_off"
  // (model.CouponType*). "fixed_amount_off" is the pure engine's
  // spelling and can appear on an order's recorded coupon_type.
  type: "percent_off" | "fixed_off"
  value_bps: number
  value_minor: number
  currency?: string
  starts_at?: string
  ends_at?: string
  max_redemptions: number
  times_redeemed: number
  max_redemptions_per_customer: number
  minimum_order_minor: number
  applies_to?: string
  stackable: boolean
  active: boolean
  created_at: string
  updated_at: string
}

// CouponInput is the create body. Update (PUT) takes the same fields;
// an omitted starts_at/ends_at means "no bound" on create and "keep
// the stored one" on update.
export interface CouponInput {
  code: string
  type: "percent_off" | "fixed_off"
  value_bps?: number
  value_minor?: number
  currency?: string
  starts_at?: string
  ends_at?: string
  max_redemptions?: number
  max_redemptions_per_customer?: number
  minimum_order_minor?: number
  applies_to?: string
  stackable?: boolean
  active?: boolean
}

export interface TaxRate {
  id: string
  jurisdiction: string
  basis_points: number
  inclusive: boolean
  country: string
  region: string
  description: string
  active: boolean
  created_at: string
  updated_at: string
}

// TaxRateInput is the create body; update (PATCH) accepts the same
// fields with active only settable on an existing rate — creation
// always starts active.
export interface TaxRateInput {
  jurisdiction: string
  basis_points?: number
  inclusive?: boolean
  country?: string
  region?: string
  description?: string
  active?: boolean
}

export interface OrderItem {
  id: string
  order_id: string
  sku?: string
  product_id?: string
  plan_id?: string
  description?: string
  quantity: number
  unit_amount_minor: number
  line_subtotal_minor: number
  line_discount_minor: number
  line_tax_minor: number
  line_total_minor: number
  created_at: string
}

export interface Order {
  id: string
  order_number: string
  customer_email: string
  customer_name?: string
  license_id?: string
  currency: string
  subtotal_minor: number
  discount_minor: number
  tax_minor: number
  total_minor: number
  coupon_code?: string
  // Recorded from the pricing engine, so an order can carry the
  // engine's "fixed_amount_off" spelling as well as the stored
  // "fixed_off" one.
  coupon_type?: string
  coupon_value_bps?: number
  coupon_value_minor?: number
  tax_jurisdiction?: string
  tax_basis_points?: number
  tax_inclusive: boolean
  status: "pending" | "paid" | "failed" | "refunded"
  payment_provider?: string
  external_id?: string
  idempotency_key?: string
  paid_at?: string
  refunded_at?: string
  created_at: string
  updated_at: string
  items?: OrderItem[]
  // The billing block (PO / invoice workflow). All are nullable
  // server-side and omitted from the JSON when empty, so a missing key
  // reads the same as an empty string.
  billing_name?: string
  billing_company?: string
  billing_address_line1?: string
  billing_address_line2?: string
  billing_city?: string
  billing_region?: string
  billing_postal_code?: string
  billing_country?: string
  customer_tax_id?: string
  po_number?: string
  billing_email?: string
}

// OrderBillingPatch is the body of PATCH /admin/orders/:id/billing.
// The three-intent convention the endpoint merges by: an ABSENT field
// keeps its stored value, a field sent as "" (or only spaces) is
// CLEARED, and a field with a value is set (trimmed, country folded
// to upper case). Build the body from changed fields only.
export interface OrderBillingPatch {
  billing_name?: string
  billing_company?: string
  billing_address_line1?: string
  billing_address_line2?: string
  billing_city?: string
  billing_region?: string
  billing_postal_code?: string
  billing_country?: string
  customer_tax_id?: string
  po_number?: string
  billing_email?: string
}

// OrderInvoice is the ledger's billing document — one per order — not
// the Stripe-shaped Invoice above, which describes a portal
// subscription invoice.
export interface OrderInvoice {
  id: string
  order_id: string
  invoice_number: string
  // The full invoice vocabulary (model.InvoiceStatus*): uncollectible
  // (given up on) and refunded round out the draft/open/paid/void core.
  status: "draft" | "open" | "paid" | "void" | "uncollectible" | "refunded"
  currency: string
  subtotal_minor: number
  discount_minor: number
  tax_minor: number
  total_minor: number
  issued_at?: string
  due_at?: string
  paid_at?: string
  created_at: string
  // Stamped by the two admin state transitions (void /
  // mark-uncollectible). Nullable and additive: a row predating them
  // simply omits them.
  voided_at?: string
  uncollectible_at?: string
}

// Refund is one row of an order's refund ledger (plan §79) — one row
// per refund issued against the order. Money is int64 minor units;
// payment_provider says who moved (or was asked to move) the money:
// stripe | pay2s | zalopay | payos | manual.
export interface Refund {
  // BIGSERIAL row id — the ledger keeps integer ids, unlike the
  // uuid TEXT ids of the entities around it.
  id: number
  order_id: string
  payment_provider: string
  // Gateway handle of the payment refunded; empty for manual refunds.
  provider_ref?: string
  trans_id?: string
  amount_minor: number
  currency: string
  reason?: string
  status: "pending" | "succeeded" | "failed"
  refunded_by?: string
  created_at: string
  updated_at: string
}

export interface QuoteLineInput {
  sku?: string
  product_id?: string
  plan_id?: string
  description?: string
  quantity: number
  unit_amount_minor: number
}

export interface QuoteRequest {
  lines: QuoteLineInput[]
  currency: string
  coupon_code?: string
  tax_inclusive?: boolean
  tax_rates?: { basis_points: number; jurisdiction: string }[]
}

export interface QuoteLineResult {
  sku?: string
  product_id?: string
  plan_id?: string
  description?: string
  quantity: number
  unit_amount_minor: number
  line_subtotal_minor: number
  line_discount_minor: number
  line_tax_minor: number
  line_total_minor: number
}

export interface QuoteResult {
  currency: string
  tax_inclusive: boolean
  subtotal_minor: number
  discount_minor: number
  tax_minor: number
  total_minor: number
  lines?: QuoteLineResult[]
  // The coupon behind discount_minor, in the engine's own spelling of
  // the type ("percent_off" / "fixed_amount_off").
  applied_coupon: { code: string; type: string; value: number; currency?: string } | null
}

// PortalDownload is one release artifact a customer is entitled to
// download. It carries metadata and pointers into the existing
// license-gated download flow — never a credential or a signed link.
export interface PortalDownload {
  license_id: string
  product_id: string
  product_name: string
  product_slug: string
  version: string
  channel: string
  platform: string
  filename: string
  file_size: number
  sha256: string
  published_at?: string
  download_url: string
}

// CustomerAPIKey is a customer's own portal API key. The secret is
// returned exactly once, on creation; only its prefix is stored and
// shown thereafter. `scopes` is a comma-separated string.
export interface CustomerAPIKey {
  id: string
  user_id: string
  name: string
  key_prefix: string
  scopes?: string
  expires_at?: string
  last_used_at?: string
  revoked_at?: string
  created_at: string
  updated_at: string
}

// ─── Checkout quote (public pricing preview) ───
// POST /checkout/quote prices a checkout — items, coupon, tax — without
// writing anything. Unit prices are resolved server-side; there is no
// amount field to send. Every amount is integer minor units.
export interface CheckoutQuoteItem {
  // Each item names its plan EITHER by the checkout_id of its payment
  // link or directly by plan_id (the cart does the latter) — never
  // both. The server prices every line; there is no amount to send.
  checkout_id?: string
  plan_id?: string
  quantity: number
}
export interface CheckoutQuoteRequest {
  items: CheckoutQuoteItem[]
  coupon_code?: string
  country?: string
  region?: string
  tax_inclusive?: boolean
}
export interface CheckoutQuoteLine {
  sku?: string
  product_id?: string
  plan_id?: string
  description?: string
  quantity: number
  unit_amount_minor: number
  line_subtotal_minor: number
  line_discount_minor: number
  line_tax_minor: number
  line_total_minor: number
}
export interface CheckoutQuoteTaxRate {
  jurisdiction: string
  basis_points: number
}
export interface CheckoutAppliedCoupon {
  code: string
  type: string
  value: number
  currency?: string
}
export interface CheckoutQuoteResult {
  currency: string
  tax_inclusive: boolean
  subtotal_minor: number
  discount_minor: number
  tax_minor: number
  total_minor: number
  lines?: CheckoutQuoteLine[]
  applied_coupon: CheckoutAppliedCoupon | null
  tax_rates?: CheckoutQuoteTaxRate[]
}

// ─── Gateway checkout (Vietnamese one-off VND payments) ───
// Pay2S (bank transfer / Napas 247 QR), ZaloPay, payOS (VietQR).
// Subscriptions stay on Stripe; gateways settle VND whole-dong amounts
// (minor units = dong — money.ts knows the VND exponent is 0).
export interface GatewayMethod {
  id: string
  name: string
}
export interface GatewayPayRequest {
  // Single-plan flow (/checkout/:checkout_id) sends plan_id; the cart
  // flow sends items. The server prices either — no amount is sent.
  plan_id?: string
  items?: { plan_id: string; quantity: number }[]
  provider: string
  coupon_code?: string
  country?: string
  // Required by the server: the licence is delivered by email, so a
  // gateway checkout without one is refused (MISSING_CUSTOMER).
  email: string
}

// ─── Cart checkout (multi-item, Stripe) ───
// POST /checkout/cart turns the cart's items into a payment link. The
// server re-prices every line; the amounts the cart carried were only
// ever display hints. {checkout_url} is where the browser goes next.
export interface CheckoutCartRequest {
  items: { plan_id: string; quantity: number }[]
  coupon_code?: string
  country?: string
  email?: string
  // Checkout attribution, the same authority the /pay/:checkout_id
  // query carries — optional and purely additive to the pinned body.
  reseller_code?: string
  ref?: string
}
export interface CheckoutCartResult {
  checkout_url: string
}
export interface GatewayPayResult {
  pay_url: string
  provider_ref: string
  order_number: string
  amount_minor: number
  currency: string
  qr_code?: string
  expires_at?: string
}

// ─── Categories (marketplace catalog) ───
// The admin category row: the catalog facet the marketplace browses
// by. `slug` is the URL handle (?category=<slug>), `position` orders
// the catalog (0 = top). Create/update accept a subset; the server
// derives a slug from the name when none is sent.
export interface Category {
  id: string
  name: string
  slug: string
  description: string
  position: number
  created_at: string
  updated_at: string
}
export interface CategoryInput {
  name: string
  slug?: string
  description?: string
  position?: number
}

// ─── Marketplace (public catalog) ───
// These mirror the anonymous storefront's JSON exactly. The §223
// catalog fields (description, short_description, logo_url, images,
// documentation_url, website_url, repository_url, vendor) are
// optional/nullable enrichment — rendered when present, tolerated
// when null or absent. `price`/`currency` on a plan are integer minor
// units / an ISO code, and are null in the marketplace payload today
// (the Stripe Price is the source of truth).
export interface MarketplaceCategory {
  id: string
  name: string
  slug: string
  description: string
  position: number
}
export interface MarketplacePlan {
  id: string
  name: string
  slug: string
  license_type: string
  billing_interval?: string
  license_model?: string
  checkout_id: string
  price: number | null
  currency: string | null
}
export interface MarketplaceArtifact {
  platform: string
  filename: string
  file_size: number
  content_type: string
  sha256: string
}
export interface MarketplaceRelease {
  version: string
  channel: string
  name: string
  release_notes: string
  published_at: string
  artifacts: MarketplaceArtifact[]
}
// ─── Resellers & affiliates (partner programs) ───
//
// Money is integer minor units and every rate is integer basis points
// (10000 = 100%) — never float. Partner commission amounts carry no
// currency (they are ledger figures), so they display as grouped
// integers via formatMinorUnits (lib/money.ts).

export interface Reseller {
  id: string
  name: string
  contact_email: string
  status: "active" | "suspended"
  // Contract share of a sale in basis points (10000 = 100%).
  commission_bps: number
  notes: string
  created_at: string
  updated_at: string
}

// Commission is one ledger row: what a reseller earned on an order.
// Basis, rate and amount are snapshotted at accrual, so the row is
// self-contained. Append-mostly: accrual creates it, the payout mark
// stamps it.
export interface Commission {
  id: string
  reseller_id: string
  order_id: string
  basis_minor: number
  bps: number
  amount_minor: number
  status: "accrued" | "approved" | "paid" | "cancelled"
  paid_at?: string
  notes: string
  created_at: string
  updated_at: string
}

// ResellerPriceOverride is the wholesale price one reseller pays for
// one plan. Configuration keyed by (reseller, plan), overwritten in
// place when the deal changes.
export interface ResellerPriceOverride {
  reseller_id: string
  plan_id: string
  unit_amount_minor: number
  currency: string
  created_at: string
  updated_at: string
}

export interface Affiliate {
  id: string
  name: string
  contact_email: string
  status: "active" | "suspended"
  // Which of the two commission fields pays: percent uses
  // commission_bps, fixed uses commission_minor. Both are stored
  // whatever the model, so switching is an edit, not a migration.
  commission_model: "percent" | "fixed"
  commission_bps: number
  commission_minor: number
  payout_method: string
  notes: string
  created_at: string
  updated_at: string
}

// ReferralCode is one shareable handle (/r/<code>, the htc_ref
// cookie). The handle is immutable; landing_url and active are the
// editable parts. Deletion is refused while conversions exist.
export interface ReferralCode {
  id: string
  affiliate_id: string
  code: string
  landing_url?: string
  active: boolean
  created_at: string
}

// AffiliateConversion is one attributed sale. Exactly one row per
// order (idempotent). Status: pending → approved|rejected|reversed;
// approved|paid can only be clawed back (reversed); paid is reached
// only through a payout settling it.
export interface AffiliateConversion {
  id: string
  affiliate_id: string
  code_id: string
  order_id: string
  user_id?: string
  order_total_minor: number
  commission_minor: number
  status: "pending" | "approved" | "paid" | "rejected" | "reversed"
  payout_id?: string
  created_at: string
  updated_at: string
}

// AffiliatePayout settles whole conversions. Only a requested payout
// moves (to paid or failed); a failed one returns its claims to the
// accrued pool.
export interface AffiliatePayout {
  id: string
  affiliate_id: string
  amount_minor: number
  status: "requested" | "paid" | "failed"
  paid_at?: string
  notes: string
  created_at: string
}

// ─── Product reviews (marketplace content) ───
//
// Ratings are whole stars 1..5 and the aggregate is integer basis
// points (rating_average_bps / average_bps: 10000 = 1 star) — the
// no-float discipline of money and rates.
//
// Review is the full moderation row (portal create/update responses
// and every admin payload). Fields the Go JSON omits when empty
// (customer_name, title, admin_reply) are optional here.
export interface Review {
  id: string
  product_id: string
  customer_email: string
  customer_name?: string
  rating: number
  title?: string
  body: string
  status: "pending" | "approved" | "rejected"
  admin_reply?: string
  created_at: string
  updated_at: string
}

// PublicReview is the anonymous storefront's selection
// (reviewPublicJSON): no author email, no moderation status —
// everything on that surface is approved by construction.
export interface PublicReview {
  id: string
  customer_name?: string
  rating: number
  title?: string
  body: string
  admin_reply?: string
  created_at: string
}

export interface MarketplaceProduct {
  id: string
  name: string
  slug: string
  type: string
  download_url?: string
  minimum_supported_version?: string
  minimum_supported_message?: string
  created_at: string
  categories: MarketplaceCategory[]
  plans: MarketplacePlan[]
  releases?: MarketplaceRelease[]
  // Approved-reviews aggregate (zero / absent when none): stars in
  // integer basis points and how many reviews they are drawn from.
  rating_average_bps?: number
  rating_count?: number
  // Enrichment (nullable / may be absent).
  description?: string | null
  short_description?: string | null
  logo_url?: string | null
  images?: string[] | null
  documentation_url?: string | null
  website_url?: string | null
  repository_url?: string | null
  vendor?: string | null
}

// ─── Reports (plan §43) ───

// The catalog: which report types the server serves and over which
// groupings. Driven from the response so the picker never hardcodes
// the list.
export interface ReportCatalog {
  report_types: string[]
  group_by: string[]
  default_group_by?: string
  export_formats?: string[]
}

// One series row: `period` (absent on the resellers / affiliates top
// lists) plus metric keys — *_count integers and *_minor money.
export type ReportRow = Record<string, string | number | null>

export interface ReportData {
  // The engine echoes the run parameters beside the data.
  report_type?: string
  from?: string
  to?: string
  group_by?: string
  series: ReportRow[]
  totals: Record<string, number>
}

// ─── Notification center (plan §88) ───

// title_key is the event name ("license.expiring"); `data` carries
// the chips shown beside the title (order number, amounts in integer
// minor units, …). priority is styled, not enumerated — an unknown
// value renders as a plain row.
export interface NotificationItem {
  id: string
  event: string
  title_key: string
  data: Record<string, unknown> | null
  link: string | null
  priority: string
  read_at: string | null
  created_at: string
}

// ─── Privacy: data export + account deletion (plan §82) ───

// AccountExport is the privacy download: one JSON document per
// person, self-describing (`format`) so a future reader can tell which
// shape they are holding. Money stays in integer minor units like
// everywhere else; secrets never appear (see notes.secret_material).
export interface AccountExport {
  format: string
  exported_at: string
  user: User
  oauth_accounts: { id: string; provider: string; provider_id: string; email?: string; created_at: string }[]
  api_keys: CustomerAPIKey[]
  customer_webhooks: PortalWebhook[]
  seats: Seat[]
  // The account's own license keys ride along explicitly — the portal
  // already shows them to their owner.
  licenses: (License & { license_key: string })[]
  orders: Order[]
  invoices: Invoice[]
  subscriptions: Subscription[]
  notifications: NotificationItem[]
  affiliate: Affiliate | null
  referral_codes: ReferralCode[]
  affiliate_conversions: AffiliateConversion[]
  notes: { secret_material: string; deletion: string }
}

// AccountAnonymizeResult is the receipt of a deletion / anonymization:
// what was revoked (counts) and what was preserved and why. The JWT
// already issued stays valid for at most 24 hours; everything the
// server can recall is revoked immediately.
export interface AccountAnonymizeResult {
  status: string // "deleted" (self-service) | "anonymized" (admin)
  user_id: string
  anonymized_email: string
  role_demoted: boolean
  revoked: {
    refresh_tokens: number
    api_keys: number
    oauth_accounts: number
    otp_codes: number
    notifications: number
    customer_webhooks: number
    role_assignments: number
    scim_identities: number
  }
  // Kept for legal, tax and accounting reasons — deletion erases the
  // identity, not the ledger.
  retained: string[]
  note: string
}

// ─── RBAC (plan §8) ───

// A custom role. The permission set is only present on the detail
// payload (GET /admin/rbac/roles/:id) — list rows omit it.
export interface Role {
  id: string
  name: string
  description: string
  is_system: boolean
  permissions?: string[]
  created_at: string
  updated_at: string
}

// ─── Site config: surfaces + payment methods (5-domain split) ───
//
// GET /api/v1/config is the flat settings map it has always been,
// EXTENDED with `surfaces` (hostnames per surface, bare — no scheme;
// empty/missing entry = that surface rides with the others) and
// `payment_methods` (the enabled gateways). Both are optional: a
// server that predates the split answers without them and this app
// runs in single-host mode, zero redirects.
export interface SiteSurfaces {
  apex?: string
  payments?: string
  dashboard?: string
  merchant?: string
  customer?: string
  verify?: string
  hooks?: string
  docs?: string
  status?: string
  go?: string
  auth?: string
  cdn?: string
}
export interface SitePaymentMethod {
  id: string
  name: string
}
export interface SiteConfigResponse {
  site_name?: string
  brand_color?: string
  logo_url?: string
  timezone?: string
  language?: string
  attribution_text?: string
  attribution_url?: string
  base_url?: string
  surfaces?: SiteSurfaces
  payment_methods?: SitePaymentMethod[]
  [key: string]: unknown
}

// ─── Global search (plan §86) ───
//
// One hit from GET /admin/search. `type` is a member of the closed
// vocabulary (product | customer | order | invoice | license |
// subscription | device | reseller | affiliate), `url` is an SPA
// route — never a credential, never an absolute URL.
export interface AdminSearchResult {
  type: string
  id: string
  title: string
  subtitle: string
  url: string
}

// ─── Config-in-DB (admin Config UI) ───
//
// The catalog GET /admin/config answers: categories of typed keys.
// A secret entry never carries its value — `value` is always empty
// and `set` says whether one is stored. `env_var` names the
// environment fallback (env is fallback-ONLY: a stored value wins),
// `restart_required` marks keys the server must be bounced for.
export interface ConfigEntry {
  key: string
  category: string
  type: "string" | "int" | "bool" | "duration" | "secret"
  value: string
  set: boolean
  secret: boolean
  default: string
  env_var: string
  description: string
  restart_required: boolean
}
export interface ConfigCategory {
  id: string
  name: string
  keys: ConfigEntry[]
}
