import { useState } from "react"
import { Link, useLocation } from "react-router"
import { Search, User, Settings, LogOut } from "lucide-react"
import { Logo } from "../Logo.tsx"
import { cn } from "../../lib/utils.ts"

export interface HeaderTab {
  id: string
  label: string
  to: string
  /** Bold emphasis, for the primary tab the app treats as its entry point. */
  highlight?: boolean
  /** External target — skips client routing. */
  external?: boolean
}

interface HeaderProps {
  tabs?: HeaderTab[]
  username?: string
  userSettingsUrl?: string
  signOutUrl?: string
  onSearchClick?: () => void
  version?: string
  scrolled?: boolean
  logoSvg?: string
  logoText?: string
  /** Logo height in px. Defaults to 36 and is clamped to a sane range so a
   *  square logo can be enlarged without a bad config value deforming the bar.
   *  Accepts a numeric string too, since branding config.json may ship "48". */
  logoHeight?: number | string
}

const DEFAULT_LOGO_HEIGHT = 36
const MIN_LOGO_HEIGHT = 16
const MAX_LOGO_HEIGHT = 80

function resolveLogoHeight(height?: number | string): number {
  const n = typeof height === "string" ? Number(height) : height
  if (typeof n !== "number" || !Number.isFinite(n)) return DEFAULT_LOGO_HEIGHT
  return Math.max(MIN_LOGO_HEIGHT, Math.min(MAX_LOGO_HEIGHT, n))
}

const DEFAULT_TABS: HeaderTab[] = [
  { id: "console", label: "Console", to: "/console", highlight: true },
  { id: "marketplace", label: "Marketplace", to: "/marketplace" },
]

export function Header({
  tabs = DEFAULT_TABS,
  username,
  userSettingsUrl,
  signOutUrl,
  onSearchClick,
  version,
  scrolled,
  logoSvg,
  logoText,
  logoHeight,
}: HeaderProps) {
  const location = useLocation()
  const [showUserMenu, setShowUserMenu] = useState(false)
  const resolvedLogoHeight = resolveLogoHeight(logoHeight)

  return (
    <header
      className={cn(
        // min-h (not a fixed h) so a taller logo grows the bar instead of
        // overflowing the border; the default 36px logo keeps the old 56px bar.
        "flex min-h-[3.5rem] shrink-0 items-center justify-between border-b border-slate-200 bg-white px-4 py-1.5 transition-shadow duration-200",
        scrolled && "shadow-sm",
      )}
    >
      <div className="flex items-center gap-6">
        <Link to="/" className="flex items-center gap-2">
          <Logo
            className="w-auto"
            style={{ height: resolvedLogoHeight }}
            svgContent={logoSvg}
            text={logoText}
          />
          {version && (
            <span className="text-xs font-medium text-slate-400">{version}</span>
          )}
        </Link>
        <nav className="flex items-center gap-1">
          {tabs.map((t) => {
            const active = location.pathname.startsWith(t.to)
            const className = cn(
              "rounded-md px-3 py-1.5 text-sm transition-colors",
              active
                ? "bg-blue-50 font-semibold text-blue-700"
                : t.highlight
                  ? "font-semibold text-slate-900 hover:bg-slate-100"
                  : "text-slate-500 hover:bg-slate-100 hover:text-slate-700",
            )
            return t.external ? (
              <a
                key={t.id}
                href={t.to}
                target="_blank"
                rel="noopener noreferrer"
                className={className}
              >
                {t.label}
              </a>
            ) : (
              <Link key={t.id} to={t.to} className={className}>
                {t.label}
              </Link>
            )
          })}
        </nav>
      </div>

      <div className="flex items-center gap-1">
        <button
          type="button"
          onClick={onSearchClick}
          className="rounded-md p-2 text-slate-500 hover:bg-slate-100 hover:text-slate-700"
        >
          <Search className="h-[18px] w-[18px]" />
        </button>
        <div className="mx-2 h-5 w-px bg-slate-200" />

        <div className="relative">
          <button
            type="button"
            onClick={() => setShowUserMenu((v) => !v)}
            className="flex items-center gap-2 rounded-md px-2 py-1 hover:bg-slate-100"
          >
            <User className="h-[18px] w-[18px] text-slate-400" />
            <span className="text-sm text-slate-600">{username ?? "\u00A0"}</span>
          </button>
          {showUserMenu && (
            <>
              <div
                className="fixed inset-0 z-40"
                onClick={() => setShowUserMenu(false)}
              />
              <div className="absolute right-0 top-10 z-50 w-48 rounded-lg border border-slate-200 bg-white py-1 shadow-lg">
                {userSettingsUrl && (
                  <a
                    href={userSettingsUrl}
                    target="_blank"
                    rel="noopener noreferrer"
                    className="flex items-center gap-2.5 px-3 py-2 text-sm text-slate-600 hover:bg-slate-50"
                    onClick={() => setShowUserMenu(false)}
                  >
                    <Settings className="h-4 w-4" />
                    User Settings
                  </a>
                )}
                <a
                  href={signOutUrl ?? "/oauth2/sign_out?rd=/"}
                  className="flex items-center gap-2.5 px-3 py-2 text-sm text-red-600 hover:bg-red-50"
                >
                  <LogOut className="h-4 w-4" />
                  Sign Out
                </a>
              </div>
            </>
          )}
        </div>
      </div>
    </header>
  )
}
