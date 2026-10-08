import type { TranslationKeys } from "@/i18n"
import en from "@/i18n/locales/en"
import { ApiError, ServiceUnavailableError } from "@/lib/api"

// Error UX (plan §90): an error toast says what happened, what to do
// next, and carries the request id when the API echoed one back — the
// reference support needs to trace the failure ("Request ID: req_xxx").
// Pages with a curated, already-actionable message (the 409 refusals)
// pass it in; everything else falls back to the server's message plus
// the next-step line.

type Translate = (key: TranslationKeys, params?: Record<string, string | number>) => string

// The active translator is bound once by the toast bridge (which sits
// inside the i18n provider) so error strings follow the UI language.
// Until then — and outside React — the English master is the fallback,
// exactly like the provider's own fallback context.
let translate: Translate = (key, params) => {
  let text: string = en[key] || key
  if (params) {
    for (const [k, v] of Object.entries(params)) {
      text = text.replace(`{${k}}`, String(v))
    }
  }
  return text
}

export function bindErrorTranslator(t: Translate): void {
  translate = t
}

// requestIdOf reads the id the middleware echoed back on the
// X-Request-ID response header and ApiError carried onto the error.
export function requestIdOf(e: unknown): string {
  if (e instanceof ApiError || e instanceof ServiceUnavailableError) return e.requestId
  return ""
}

function refSuffix(e: unknown): string {
  const id = requestIdOf(e)
  return id ? ` ${translate("error.requestId", { id })}` : ""
}

// withRequestRef appends the request reference to a curated message —
// the 409 refusals already say what to do, they only need the id.
export function withRequestRef(message: string, e: unknown): string {
  return `${message}${refSuffix(e)}`
}

// errorMessage is the fallback shape: what happened (the server's
// message says exactly what was refused), what to do next, and the
// reference to quote. A transport failure replaces the head with its
// own actionable line — "Failed to fetch" tells nobody anything.
export function errorMessage(e: unknown): string {
  const ref = refSuffix(e)
  if (e instanceof ServiceUnavailableError) return `${translate("error.unavailable")}${ref}`
  const head = e instanceof Error && e.message ? e.message : translate("error.unexpected")
  return `${head} ${translate("error.whatNext")}${ref}`
}

// Errors a page has already raised a toast for. The global mutation
// hook must not add a second toast on top of a page's curated one.
// (The mark is written by toastError in components/toast — the one
// dependency direction is toast → errors, never the reverse.)
const handled = new WeakSet<object>()

export function markErrorHandled(e: unknown): void {
  if (typeof e === "object" && e !== null) handled.add(e)
}

export function isErrorHandled(e: unknown): boolean {
  return typeof e === "object" && e !== null && handled.has(e)
}
