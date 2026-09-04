import { Navigate, Route, Routes, useLocation } from "react-router"
import { AppShell } from "@cozystack/ui"
import { TenantProvider } from "./lib/tenant-context.tsx"
import { Breadcrumb } from "./components/Breadcrumb.tsx"
import { MarketplacePage } from "./routes/MarketplacePage.tsx"
import { ConsolePage } from "./routes/ConsolePage.tsx"
import { AdminPage } from "./routes/AdminPage.tsx"
import {
  useAdminSidebarSections,
  useCanSeeAdmin,
  useConsoleSidebarSections,
  useMarketplaceSidebarSections,
} from "./routes/sidebar-sections.tsx"
import type { HeaderTab } from "@cozystack/ui"
import { CommandPaletteProvider, useCommandPalette } from "./components/command-palette/command-palette-provider.tsx"
import { CommandPalette } from "./components/command-palette/command-palette.tsx"
import type { AppConfig } from "./lib/config.ts"
import { DEFAULT_LANDING_PATH } from "./lib/portal.ts"

// Admin pages whose content does not depend on the selected tenant, so the
// picker has nothing to say on them. /admin/tenants is deliberately absent:
// its rows come from the picker-filtered context list, unlike Modules and
// External IPs which list tenant namespaces themselves.
const CLUSTER_SCOPED_ADMIN = [
  "/admin/capacity",
  "/admin/backups/backupclasses",
  "/admin/external-ips",
  "/admin/modules",
]

interface ShellProps {
  config: AppConfig
  username?: string
}

function Shell({ config, username }: ShellProps) {
  const { pathname } = useLocation()
  const inMarketplace = pathname.startsWith("/marketplace")
  const inAdmin = pathname.startsWith("/admin")
  // Only the genuinely cluster-scoped admin pages have no tenant to show. The
  // rest of /admin mounts the same tenant-scoped resource pages as /console
  // (see lib/portal.ts), and those resolve their namespace from the tenant
  // context, so hiding the picker there would leave the active tenant both
  // invisible and unchangeable.
  // Anchored on a segment boundary: an unanchored prefix would also swallow a
  // sibling route that merely starts with the same characters.
  const inClusterScope = CLUSTER_SCOPED_ADMIN.some(
    (prefix) => pathname === prefix || pathname.startsWith(`${prefix}/`),
  )
  const marketplaceSections = useMarketplaceSidebarSections()
  const consoleSections = useConsoleSidebarSections()
  const adminSections = useAdminSidebarSections()
  const canSeeAdmin = useCanSeeAdmin()
  const sections = inAdmin
    ? adminSections
    : inMarketplace
      ? marketplaceSections
      : consoleSections
  const { toggle } = useCommandPalette()

  const tabs: HeaderTab[] = [
    { id: "console", label: "Console", to: "/console", highlight: true },
    { id: "marketplace", label: "Marketplace", to: "/marketplace" },
    ...(canSeeAdmin ? [{ id: "admin", label: "Admin", to: "/admin" }] : []),
  ]

  return (
    <AppShell
      tabs={tabs}
      sections={sections}
      subtitle={inClusterScope ? undefined : <Breadcrumb />}
      onSearchClick={toggle}
      version={config.version || import.meta.env.VITE_APP_VERSION}
      logoSvg={config.logoSvg}
      logoText={config.logoText}
      username={username}
    >
      <CommandPalette />
      <Routes>
        <Route path="/" element={<Navigate to={DEFAULT_LANDING_PATH} replace />} />
        <Route path="/marketplace/*" element={<MarketplacePage />} />
        <Route path="/console/*" element={<ConsolePage />} />
        <Route path="/admin/*" element={<AdminPage />} />
      </Routes>
    </AppShell>
  )
}

export interface AppProps {
  config?: AppConfig
  username?: string
}

export default function App({ config = {}, username }: AppProps) {
  return (
    <TenantProvider>
      <CommandPaletteProvider>
        <Shell config={config} username={username} />
      </CommandPaletteProvider>
    </TenantProvider>
  )
}
