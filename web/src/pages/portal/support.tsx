import { HelpCircle, LifeBuoy, Mail } from "lucide-react"
import { Card, CardContent, CardHeader, CardTitle } from "@/components/ui/card"
import { useAuth } from "@/hooks/use-auth"
import { useSiteConfig } from "@/hooks/use-site-config"
import { useI18n } from "@/i18n"

// A deliberately small support surface: how to reach the vendor and a
// short FAQ, seeded from the site config (site_name) and the signed-in
// account's contact address. No backend support desk is assumed.
const FAQ_KEYS = [
  ["portal.supportFaq1Q", "portal.supportFaq1A"],
  ["portal.supportFaq2Q", "portal.supportFaq2A"],
  ["portal.supportFaq3Q", "portal.supportFaq3A"],
] as const

export default function PortalSupportPage() {
  const { t } = useI18n()
  const { site_name } = useSiteConfig()
  const { user } = useAuth()

  return (
    <div className="space-y-6">
      <div>
        <h1 className="text-2xl font-bold tracking-tight">{t("portal.supportTitle")}</h1>
        <p className="text-muted-foreground">{t("portal.supportDesc")}</p>
      </div>

      <div className="grid gap-4 lg:grid-cols-2">
        {/* Contact */}
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-lg">
              <LifeBuoy className="h-5 w-5 text-muted-foreground" /> {t("portal.supportContactTitle")}
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-3">
            <p className="text-sm text-muted-foreground">{t("portal.supportContactDesc", { vendor: site_name })}</p>
            {user?.email && (
              <div className="flex items-center gap-2 rounded-lg border px-3 py-2 text-sm">
                <Mail className="h-4 w-4 text-muted-foreground shrink-0" />
                <span className="truncate">{user.email}</span>
              </div>
            )}
            <p className="text-xs text-muted-foreground">{t("portal.supportResponseNote")}</p>
          </CardContent>
        </Card>

        {/* FAQ */}
        <Card>
          <CardHeader>
            <CardTitle className="flex items-center gap-2 text-lg">
              <HelpCircle className="h-5 w-5 text-muted-foreground" /> {t("portal.supportFaqTitle")}
            </CardTitle>
          </CardHeader>
          <CardContent className="space-y-2">
            {FAQ_KEYS.map(([q, a]) => (
              <details key={q} className="rounded-lg border px-3 py-2">
                <summary className="cursor-pointer text-sm font-medium select-none">{t(q)}</summary>
                <p className="mt-2 text-sm text-muted-foreground">{t(a)}</p>
              </details>
            ))}
          </CardContent>
        </Card>
      </div>
    </div>
  )
}
