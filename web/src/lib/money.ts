// ─── Money & percent helpers (commerce) ───
//
// Money on this platform is integer minor units and percentages are
// integer basis points (10000 = 100%) — the same units the Go engine
// stores (internal/money, internal/coupon). Nothing here turns them
// into a float for arithmetic: display splits the integer into whole
// and fractional digits as strings, and the parsers accept only digit
// strings, so a percent entered as "12.5" comes back out as 1250 basis
// points and round-trips exactly.

// The supported ISO 4217 codes and their minor-unit exponent (the
// number of decimal digits that make up one major unit), mirroring
// money.currencyExponents on the server so a total is shown at the
// scale it was stored at. An unknown code falls back to 2 — the common
// case — and is only ever a display guess; the server refuses to do
// money in a currency it does not know.
const CURRENCY_EXPONENTS: Record<string, number> = {
  AUD: 2,
  CAD: 2,
  CHF: 2,
  CNY: 2,
  EUR: 2,
  GBP: 2,
  IDR: 0,
  INR: 2,
  JPY: 0,
  KRW: 0,
  PHP: 2,
  SGD: 2,
  THB: 2,
  USD: 2,
  VND: 0,
}

export function currencyExponent(currency: string): number {
  return CURRENCY_EXPONENTS[currency.trim().toUpperCase()] ?? 2
}

// formatMinor renders integer minor units as "1,234.56 USD". The
// digits come from the integer itself — the whole part is grouped via
// BigInt, so even a large ledger figure cannot pick up float error —
// and the currency code names the scale ("123,456 VND" at exponent 0).
export function formatMinor(minor: number, currency: string): string {
  const code = currency.trim().toUpperCase()
  const exp = currencyExponent(code)
  const negative = minor < 0
  const digits = Math.trunc(Math.abs(minor))
    .toString()
    .padStart(exp + 1, "0")
  const whole = digits.slice(0, digits.length - exp) || "0"
  const frac = exp > 0 ? digits.slice(digits.length - exp) : ""
  const grouped = new Intl.NumberFormat().format(BigInt(whole))
  return `${negative ? "-" : ""}${grouped}${frac ? `.${frac}` : ""} ${code}`
}

// bpsToPercentString turns basis points into the percent an admin
// thinks in: 1250 → "12.5", 1205 → "12.05", 10000 → "100". Trailing
// zeros are trimmed so a value displays the same way it was typed.
export function bpsToPercentString(bps: number): string {
  const sign = bps < 0 ? "-" : ""
  const abs = Math.trunc(Math.abs(bps))
  const whole = Math.trunc(abs / 100)
  const frac = abs % 100
  if (frac === 0) return `${sign}${whole}`
  return `${sign}${whole}.${String(frac).padStart(2, "0").replace(/0$/, "")}`
}

export function formatBps(bps: number): string {
  return `${bpsToPercentString(bps)}%`
}

// percentStringToBps parses "12.5" into 1250 basis points by reading
// the digits either side of the point — no multiplication of floats, so
// every value that displays can be typed back unchanged. Returns null
// for anything that is not a plain decimal with at most two decimal
// places; the caller turns that into a field error.
export function percentStringToBps(input: string): number | null {
  const m = /^(\d+)(?:\.(\d{1,2}))?$/.exec(input.trim())
  if (!m) return null
  const whole = Number(m[1])
  const frac = m[2] ? Number(m[2].padEnd(2, "0")) : 0
  const bps = whole * 100 + frac
  return Number.isSafeInteger(bps) ? bps : null
}

// parseIntStrict accepts a non-negative integer as typed into a minor-
// units box. No sign, no decimal point: "12.50" is not 1250 cents, it
// is a mistake, and the caller should say so rather than guess.
export function parseIntStrict(input: string): number | null {
  const s = input.trim()
  if (!/^\d+$/.test(s)) return null
  const n = Number(s)
  return Number.isSafeInteger(n) ? n : null
}
