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

interface ShellProps {
  config: AppConfig
  username?: string
}

function Shell({ config, username }: ShellProps) {
  const { pathname } = useLocation()
  const inMarketplace = pathname.startsWith("/marketplace")
  const inAdmin = pathname.startsWith("/admin")
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
    { id: "marketplace", label: "Marketplace", to: "/marketplace", highlight: true },
    { id: "console", label: "Console", to: "/console" },
    ...(canSeeAdmin ? [{ id: "admin", label: "Admin", to: "/admin" }] : []),
  ]

  return (
    <AppShell
      tabs={tabs}
      sections={sections}
      subtitle={<Breadcrumb />}
      onSearchClick={toggle}
      version={config.version || import.meta.env.VITE_APP_VERSION}
      logoSvg={config.logoSvg}
      logoText={config.logoText}
      username={username}
    >
      <CommandPalette />
      <Routes>
        <Route path="/" element={<Navigate to="/marketplace" replace />} />
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
