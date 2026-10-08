import { ShoppingCart } from "lucide-react"
import { Link } from "react-router-dom"
import { useI18n } from "@/i18n"
import { useCart } from "@/lib/cart"

// CartBadge — the header's cart link with its item count. Rendered by
// the storefront shells; hidden entirely when the cart is empty so a
// first visit to the marketplace does not carry a dead zero.
export function CartBadge() {
  const { t } = useI18n()
  const { count } = useCart()
  return (
    <Link
      to="/cart"
      aria-label={t("cart.badge", { count })}
      className="relative rounded-md outline-none focus-visible:ring-2 focus-visible:ring-ring"
    >
      <span className="flex h-9 items-center gap-1.5 rounded-md px-2 text-sm text-muted-foreground transition-colors hover:bg-accent hover:text-accent-foreground">
        <ShoppingCart className="h-4 w-4" />
        <span className="hidden sm:inline">{t("cart.title")}</span>
      </span>
      {count > 0 && (
        <span className="absolute -right-0.5 -top-0.5 flex h-4 min-w-4 items-center justify-center rounded-full bg-primary px-1 text-[10px] font-semibold text-primary-foreground">
          {count > 99 ? "99+" : count}
        </span>
      )}
    </Link>
  )
}
