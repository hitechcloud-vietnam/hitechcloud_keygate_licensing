// ─── Cart (plan §23, client side) ───
//
// A localStorage cart: the marketplace "add to plan" buttons fill it,
// the cart page edits it, and the checkout page prices it with the
// server's quote before payment. Every amount in here is a DISPLAY
// HINT in integer minor units taken from the quote the visitor saw —
// the server re-prices everything at checkout and its numbers win.
// Money discipline (lib/money.ts) holds: no float math, ever.
//
// Shape on disk (localStorage["hitechcloud_cart"]):
//
//	{items: [{plan_id, product_name, plan_name, quantity,
//	          unit_amount_minor, currency}]}
//
// Cross-host note (the 5-domain split): localStorage does not follow
// the browser to another hostname, so the cart page encodes the
// plan_id/quantity pairs into the checkout URL when it hands off to a
// payments surface (encodeCartItems). The display hints stay behind;
// the quote on the far side describes the lines.

import { useSyncExternalStore } from "react"

export interface CartItem {
  plan_id: string
  product_name: string
  plan_name: string
  quantity: number
  unit_amount_minor: number
  currency: string
}

export const CART_STORAGE_KEY = "hitechcloud_cart"

// loadCart reads the stored cart, tolerating anything: a corrupt or
// half-written entry is dropped, never thrown at the page. Rows that
// carry no plan_id or a non-positive quantity are not cart lines.
function loadCart(): CartItem[] {
  try {
    const raw = localStorage.getItem(CART_STORAGE_KEY)
    if (!raw) return []
    const parsed = JSON.parse(raw)
    const items = Array.isArray(parsed?.items) ? parsed.items : []
    const out: CartItem[] = []
    for (const it of items) {
      if (typeof it?.plan_id !== "string" || it.plan_id === "") continue
      const qty = Math.trunc(Number(it?.quantity))
      if (!Number.isFinite(qty) || qty < 1) continue
      out.push({
        plan_id: it.plan_id,
        product_name: typeof it?.product_name === "string" ? it.product_name : "",
        plan_name: typeof it?.plan_name === "string" ? it.plan_name : "",
        quantity: Math.min(qty, 999),
        unit_amount_minor: Math.trunc(Number(it?.unit_amount_minor)) || 0,
        currency: typeof it?.currency === "string" ? it.currency : "",
      })
    }
    return out
  } catch {
    return []
  }
}

// The React-facing store. One cache + a listener set, so every hook
// sees the same cart and re-renders when it changes — including a
// change made in another tab (the storage event).
let cache: CartItem[] | null = null
const listeners = new Set<() => void>()

function snapshot(): CartItem[] {
  cache ??= loadCart()
  return cache
}

function emit(): void {
  for (const fn of listeners) fn()
}

function save(items: CartItem[]): void {
  cache = items
  try {
    localStorage.setItem(CART_STORAGE_KEY, JSON.stringify({ items }))
  } catch {
    // Storage full or blocked (private mode): the in-memory cart
    // still works for this page, it just will not survive a reload.
  }
  emit()
}

function subscribe(fn: () => void): () => void {
  listeners.add(fn)
  return () => listeners.delete(fn)
}

// Keep tabs in step: the storage event fires in the OTHER tabs of the
// same origin when one of them writes the cart.
if (typeof window !== "undefined") {
  window.addEventListener("storage", (e) => {
    if (e.key === CART_STORAGE_KEY) {
      cache = null
      emit()
    }
  })
}

// addCartItem merges by plan_id — adding the same plan twice bumps the
// quantity — and caps the line at 999 so a stuck stepper cannot ask
// the server for a million seats.
export function addCartItem(item: Omit<CartItem, "quantity"> & { quantity?: number }): void {
  const items = snapshot().slice()
  const qty = Math.max(1, Math.trunc(item.quantity ?? 1))
  const found = items.findIndex((i) => i.plan_id === item.plan_id)
  if (found >= 0) {
    items[found] = { ...items[found], quantity: Math.min(999, items[found].quantity + qty) }
  } else {
    items.push({
      plan_id: item.plan_id,
      product_name: item.product_name,
      plan_name: item.plan_name,
      quantity: Math.min(999, qty),
      unit_amount_minor: item.unit_amount_minor,
      currency: item.currency,
    })
  }
  save(items)
}

export function setCartQuantity(planId: string, quantity: number): void {
  const qty = Math.trunc(quantity)
  const items = snapshot()
    .filter((i) => i.plan_id !== planId || qty > 0)
    .map((i) => (i.plan_id === planId ? { ...i, quantity: Math.min(999, qty) } : i))
  save(items)
}

export function removeCartItem(planId: string): void {
  save(snapshot().filter((i) => i.plan_id !== planId))
}

export function clearCart(): void {
  save([])
}

// useCart is the hook the badge, the product page and the cart page
// read through. The snapshot is referentially stable between writes,
// so it is safe in dependency arrays.
export function useCart() {
  const items = useSyncExternalStore(subscribe, snapshot, snapshot)
  return {
    items,
    count: items.reduce((n, i) => n + i.quantity, 0),
    add: addCartItem,
    setQuantity: setCartQuantity,
    remove: removeCartItem,
    clear: clearCart,
  }
}

// ─── URL hand-off ─────────────────────────────────────────────────────
//
// Only plan_id and quantity travel in a URL; names and prices are
// display hints and are re-derived from the quote on the far side.

// encodeCartItems renders "plan_id:quantity,plan_id:quantity" for a
// checkout link. Empty cart → "".
export function encodeCartItems(items: Pick<CartItem, "plan_id" | "quantity">[]): string {
  return items
    .filter((i) => i.plan_id && i.quantity > 0)
    .map((i) => `${encodeURIComponent(i.plan_id)}:${i.quantity}`)
    .join(",")
}

// decodeCartItems reads that encoding back, ignoring any malformed
// segment rather than failing the whole hand-off.
export function decodeCartItems(raw: string): { plan_id: string; quantity: number }[] {
  const out: { plan_id: string; quantity: number }[] = []
  for (const part of raw.split(",")) {
    const [idRaw, qtyRaw] = part.split(":")
    const plan_id = decodeURIComponent(idRaw || "").trim()
    const quantity = Math.trunc(Number(qtyRaw))
    if (plan_id && Number.isFinite(quantity) && quantity > 0) {
      out.push({ plan_id, quantity: Math.min(999, quantity) })
    }
  }
  return out
}
