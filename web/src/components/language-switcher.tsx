import { Check, Languages } from "lucide-react"
import { Button } from "@/components/ui/button"
import { DropdownMenu, DropdownMenuContent, DropdownMenuItem, DropdownMenuTrigger } from "@/components/ui/dropdown-menu"
import { type Locale, useI18n } from "@/i18n"

// Language names spelled in their own language (endonyms). A language
// picker is the one place where translating the labels would be wrong:
// "Tiếng Việt" reads the same in every interface language, and it is
// how a Vietnamese speaker finds their own entry in a list of three.
const LOCALE_LABELS: Record<Locale, string> = {
  en: "English",
  vi: "Tiếng Việt",
  zh: "中文",
}

const LOCALE_ORDER: Locale[] = ["en", "vi", "zh"]

// LanguageSwitcher (plan §83 + §84): the small control in the header
// and user menus. The choice persists through setLocale into the
// hitechcloud_locale storage key the i18n provider restores on load.
// iconOnly fits the narrow admin header and the sidebar footer row;
// the labelled variant sits in the roomier portal/public headers.
export function LanguageSwitcher({ iconOnly = false, align = "end" }: { iconOnly?: boolean; align?: "start" | "end" }) {
  const { locale, setLocale, t } = useI18n()

  return (
    <DropdownMenu>
      <DropdownMenuTrigger asChild>
        <Button
          variant="ghost"
          size="sm"
          className={iconOnly ? "h-9 w-9 shrink-0 px-0" : "gap-2"}
          // The globe alone says nothing to a screen reader; the label
          // names the control and its current value.
          aria-label={`${t("nav.language")}: ${LOCALE_LABELS[locale]}`}
          title={t("nav.language")}
        >
          <Languages className="h-4 w-4" aria-hidden="true" />
          {/* The name hides below sm so the narrow portal/public
              headers keep room; the aria-label above still says it. */}
          {!iconOnly && <span className="hidden text-sm sm:inline">{LOCALE_LABELS[locale]}</span>}
        </Button>
      </DropdownMenuTrigger>
      <DropdownMenuContent align={align}>
        {LOCALE_ORDER.map((l) => (
          <DropdownMenuItem
            key={l}
            // menuitemradio semantics: one of the three is always the
            // current language, and the check mark shows which.
            role="menuitemradio"
            aria-checked={locale === l}
            onClick={() => setLocale(l)}
            className="gap-2"
          >
            {/* The check is decoration beside aria-checked — a screen
                reader already announced the state. */}
            <Check className={`h-4 w-4 shrink-0 ${locale === l ? "opacity-100" : "opacity-0"}`} aria-hidden="true" />
            {LOCALE_LABELS[l]}
          </DropdownMenuItem>
        ))}
      </DropdownMenuContent>
    </DropdownMenu>
  )
}
