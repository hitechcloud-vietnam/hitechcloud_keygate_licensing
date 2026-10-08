import { MutationCache, QueryClient, QueryClientProvider } from "@tanstack/react-query"
import { StrictMode } from "react"
import { createRoot } from "react-dom/client"
import { BrowserRouter, Navigate, Route, Routes } from "react-router-dom"
import { ErrorBoundary } from "@/components/error-boundary"
import { AdminLayout, PortalLayout, PublicLayout } from "@/components/layout"
import { showToast, ToastBridge, ToastProvider } from "@/components/toast"
import { AuthProvider } from "@/hooks/use-auth"
import { SiteConfigProvider } from "@/hooks/use-site-config"
import { I18nProvider } from "@/i18n"
import AcceptInvitePage from "@/pages/accept-invite"
import AddonsPage from "@/pages/admin/addons"
import AnalyticsPage from "@/pages/admin/analytics"
import APIKeysPage from "@/pages/admin/api-keys"
import AuditPage from "@/pages/admin/audit"
import CategoriesPage from "@/pages/admin/categories"
import CouponsPage from "@/pages/admin/coupons"
import CustomersPage from "@/pages/admin/customers"
import DashboardPage from "@/pages/admin/dashboard"
import LicensesPage from "@/pages/admin/licenses"
import OrderDetailPage from "@/pages/admin/order-detail"
import OrdersPage from "@/pages/admin/orders"
import PlansPage from "@/pages/admin/plans"
import ProductsPage from "@/pages/admin/products"
import ReleasesPage from "@/pages/admin/releases"
import SettingsPage from "@/pages/admin/settings"
import TaxRatesPage from "@/pages/admin/tax-rates"
import WebhooksPage from "@/pages/admin/webhooks"
import CheckoutPage from "@/pages/checkout"
import CheckoutSuccessPage from "@/pages/checkout-success"
import LoginPage from "@/pages/login"
import MarketplacePage from "@/pages/marketplace"
import MarketplaceProductPage from "@/pages/marketplace-product"
import PortalAccountPage from "@/pages/portal/account"
import PortalAPIKeysPage from "@/pages/portal/api-keys"
import PortalDownloadsPage from "@/pages/portal/downloads"
import PortalLicensesPage from "@/pages/portal/licenses"
import PortalOrdersPage from "@/pages/portal/orders"
import SetupPage from "@/pages/setup"
import "./index.css"

const queryClient = new QueryClient({
  mutationCache: new MutationCache({
    onError: (error) => {
      showToast(error instanceof Error ? error.message : "An error occurred")
    },
  }),
  defaultOptions: {
    queries: { retry: 1, refetchOnWindowFocus: false, staleTime: 30_000 },
  },
})

createRoot(document.getElementById("root")!).render(
  <StrictMode>
    <QueryClientProvider client={queryClient}>
      <BrowserRouter>
        <I18nProvider>
          <ToastProvider>
            <ToastBridge />
            <SiteConfigProvider>
              <AuthProvider>
                <ErrorBoundary>
                  <Routes>
                    <Route path="/setup" element={<SetupPage />} />
                    <Route path="/login" element={<LoginPage />} />
                    <Route path="/checkout/success" element={<CheckoutSuccessPage />} />
                    <Route path="/checkout/:checkout_id" element={<CheckoutPage />} />
                    <Route path="/accept-invite" element={<AcceptInvitePage />} />

                    {/* Marketplace (public storefront, anonymous) */}
                    <Route path="/marketplace" element={<PublicLayout />}>
                      <Route index element={<MarketplacePage />} />
                      <Route path="products/:slug" element={<MarketplaceProductPage />} />
                    </Route>

                    {/* Admin */}
                    <Route path="/admin" element={<AdminLayout />}>
                      <Route index element={<DashboardPage />} />
                      <Route path="products" element={<ProductsPage />} />
                      <Route path="categories" element={<CategoriesPage />} />
                      <Route path="plans" element={<PlansPage />} />
                      <Route path="releases" element={<ReleasesPage />} />
                      <Route path="licenses" element={<LicensesPage />} />
                      <Route path="api-keys" element={<APIKeysPage />} />
                      <Route path="webhooks" element={<WebhooksPage />} />
                      <Route path="addons" element={<AddonsPage />} />
                      <Route path="coupons" element={<CouponsPage />} />
                      <Route path="tax-rates" element={<TaxRatesPage />} />
                      <Route path="orders" element={<OrdersPage />} />
                      <Route path="orders/:id" element={<OrderDetailPage />} />
                      <Route path="analytics" element={<AnalyticsPage />} />
                      <Route path="audit" element={<AuditPage />} />
                      <Route path="customers" element={<CustomersPage />} />
                      <Route path="settings" element={<SettingsPage />} />
                    </Route>

                    {/* Portal */}
                    <Route path="/portal" element={<PortalLayout />}>
                      <Route index element={<PortalLicensesPage />} />
                      <Route path="orders" element={<PortalOrdersPage />} />
                      <Route path="downloads" element={<PortalDownloadsPage />} />
                      <Route path="api-keys" element={<PortalAPIKeysPage />} />
                      <Route path="account" element={<PortalAccountPage />} />
                    </Route>

                    <Route path="*" element={<Navigate to="/login" replace />} />
                  </Routes>
                </ErrorBoundary>
              </AuthProvider>
            </SiteConfigProvider>
          </ToastProvider>
        </I18nProvider>
      </BrowserRouter>
    </QueryClientProvider>
  </StrictMode>,
)
