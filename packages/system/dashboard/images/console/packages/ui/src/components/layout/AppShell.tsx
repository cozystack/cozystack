import { useState, type ReactNode } from "react"
import { Outlet } from "react-router"
import { Header, type HeaderTab } from "./Header.tsx"
import { Sidebar, type SidebarSection } from "./Sidebar.tsx"

interface AppShellProps {
  sections: SidebarSection[]
  /** Small breadcrumb-like bar below the header (tenant selector, etc.). */
  subtitle?: ReactNode
  tabs?: HeaderTab[]
  username?: string
  userSettingsUrl?: string
  signOutUrl?: string
  onSearchClick?: () => void
  version?: string
  children?: ReactNode
  logoSvg?: string
  logoText?: string
}

export function AppShell({
  sections,
  subtitle,
  tabs,
  username,
  userSettingsUrl,
  signOutUrl,
  onSearchClick,
  version,
  children,
  logoSvg,
  logoText,
}: AppShellProps) {
  const [scrolled, setScrolled] = useState(false)

  return (
    <div className="flex h-screen flex-col bg-slate-50">
      <Header
        tabs={tabs}
        username={username}
        userSettingsUrl={userSettingsUrl}
        signOutUrl={signOutUrl}
        onSearchClick={onSearchClick}
        version={version}
        scrolled={scrolled}
        logoSvg={logoSvg}
        logoText={logoText}
      />
      <div className="flex flex-1 overflow-hidden">
        <Sidebar sections={sections} />
        <div className="flex flex-1 flex-col overflow-hidden">
          {subtitle && (
            <div className="flex items-center gap-1.5 border-b border-slate-200 bg-white px-6 py-2 text-sm text-slate-600">
              {subtitle}
            </div>
          )}
          <main
            className="flex-1 overflow-auto"
            onScroll={(e) => setScrolled((e.currentTarget as HTMLElement).scrollTop > 0)}
          >
            {children ?? <Outlet />}
          </main>
        </div>
      </div>
    </div>
  )
}
