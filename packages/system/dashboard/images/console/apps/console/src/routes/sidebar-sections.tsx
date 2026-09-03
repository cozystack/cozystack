import { useMemo } from "react"
import {
  Archive,
  Cloud,
  Database,
  Gauge,
  Globe,
  HardDrive,
  LayoutGrid,
  Layers,
  Network,
  Server,
  ToyBrick,
  Users,
  type LucideIcon,
} from "lucide-react"
import type { SidebarSection } from "@cozystack/ui"
import { useBackupClassAdminAccess } from "../hooks/useBackupClassAdminAccess.ts"
import { useClusterUsageAccess } from "../hooks/useClusterUsageAccess.ts"
import { useApplicationDefinitions, groupByCategory, isTenantModule } from "../lib/app-definitions.ts"
import { humanizeKind } from "../lib/humanize.ts"
import {
  lucideIcon,
  simpleIconComponent,
  simpleIconSlug,
} from "../lib/sidebar-icons.tsx"
import type { ComponentType } from "react"

const MARKETPLACE_CATEGORIES = ["IaaS", "PaaS", "NaaS"]
const CATEGORY_ICON: Record<string, LucideIcon> = {
  IaaS: Cloud,
  PaaS: Database,
  NaaS: Network,
}

/**
 * Marketplace sidebar: a flat list of filters — "All applications" followed
 * by the three categories. Category links rely on pathname-based matching
 * (`/marketplace/c/<category>`) so NavLink correctly highlights the active
 * entry; see MarketplaceList for the counterpart.
 */
export function useMarketplaceSidebarSections(): SidebarSection[] {
  const { data } = useApplicationDefinitions()
  const grouped = useMemo(() => groupByCategory(data), [data])

  return useMemo<SidebarSection[]>(() => {
    const available = grouped
      .map((g) => g.category)
      .filter((c) => MARKETPLACE_CATEGORIES.includes(c))
    const ordered = MARKETPLACE_CATEGORIES.filter((c) => available.includes(c))

    return [
      {
        title: "Marketplace",
        items: [
          { label: "Marketplace", to: "/marketplace", end: true, icon: LayoutGrid },
          ...ordered.map((category) => ({
            label: category,
            to: `/marketplace/c/${encodeURIComponent(category)}`,
            icon: CATEGORY_ICON[category] ?? LayoutGrid,
          })),
        ],
      },
    ]
  }, [grouped])
}

/**
 * Console sidebar: every non-module, non-Tenant ApplicationDefinition in the
 * cluster as a separate entry grouped by IaaS / PaaS / NaaS, plus the per-tenant
 * Backups section. Administration (Info, Modules, External IPs, Tenants) now
 * lives in the Admin portal — see {@link useAdminSidebarSections}.
 */
export function useConsoleSidebarSections(): SidebarSection[] {
  const { data } = useApplicationDefinitions()
  const grouped = useMemo(() => groupByCategory(data), [data])

  return useMemo<SidebarSection[]>(() => {
    const sorted = [...grouped]
      .filter(({ category }) => MARKETPLACE_CATEGORIES.includes(category))
      .sort(
        (a, b) =>
          MARKETPLACE_CATEGORIES.indexOf(a.category) -
          MARKETPLACE_CATEGORIES.indexOf(b.category),
      )

    const categorySections: SidebarSection[] = sorted.map(({ category, items }) => ({
      title: category,
      items: items.map((ad) => {
        const kind = ad.spec?.application.kind ?? ad.metadata.name
        const plural = ad.spec?.application.plural ?? ad.metadata.name
        const slug = simpleIconSlug(kind)
        const lucide = lucideIcon(kind)
        const icon: ComponentType<{ className?: string }> | LucideIcon =
          slug ? simpleIconComponent(slug) : (lucide ?? Layers)
        return {
          label: humanizeKind(kind),
          to: `/console/${plural}`,
          icon,
        }
      }),
    }))

    const backupsSection: SidebarSection = {
      title: "Backups",
      items: [
        { label: "Plans", to: "/console/backups/plans", icon: Archive },
        { label: "Backup Jobs", to: "/console/backups/backupjobs", icon: Archive },
        { label: "Backups", to: "/console/backups/backups", icon: Archive },
        { label: "Restore Jobs", to: "/console/backups/restorejobs", icon: Archive },
      ],
    }

    return [...categorySections, backupsSection]
  }, [grouped])
}

/**
 * Access check for the Admin portal. The portal hosts two cluster-wide
 * operator areas with independent permissions: Cluster Usage (proxied by
 * `nodes/list`) and Backup Classes (`backupclasses/update`, via
 * {@link useBackupClassAdminAccess}). A user sees the portal if they can use
 * at least one. `isLoading` lets route guards wait instead of redirecting
 * mid-flight; the per-area booleans gate the individual sidebar entries.
 */
export function useAdminAccess(): {
  allowed: boolean
  isLoading: boolean
  canClusterUsage: boolean
  canBackupClasses: boolean
} {
  const clusterUsage = useClusterUsageAccess()
  const backupClasses = useBackupClassAdminAccess()
  const canClusterUsage = clusterUsage.allowed
  const canBackupClasses = backupClasses.allowed
  return {
    isLoading: clusterUsage.isLoading || backupClasses.isLoading,
    allowed: canClusterUsage || canBackupClasses,
    canClusterUsage,
    canBackupClasses,
  }
}

/**
 * The Admin tab is always visible: Administration (Info, Modules, External IPs,
 * Tenants) needs no special permission. The gated Capacity/Backup-Classes areas
 * keep their own per-area guards inside the portal.
 */
export function useCanSeeAdmin(): boolean {
  return true
}

/**
 * Admin sidebar: Administration first (always visible, no permission needed —
 * Tenants, Modules, External IPs; per-tenant Info is a row action in the
 * Tenants tree), then the cluster-wide operator areas (Capacity and Backup
 * Classes). Each operator area is gated by its own permission so the sidebar
 * never shows an area the user cannot open.
 */
export function useAdminSidebarSections(): SidebarSection[] {
  const { canClusterUsage, canBackupClasses } = useAdminAccess()
  const { data: defs } = useApplicationDefinitions()
  // Per-tenant Info lives as a row action in the Tenants tree, so the Tenants
  // entry stays highlighted while an Info detail page is open. Every other
  // module's detail page keeps Modules highlighted instead.
  const infoPlural =
    defs?.items.find((d) => d.spec?.application.kind === "Info")?.spec?.application
      .plural ?? "infos"
  const modulePaths = (defs?.items ?? [])
    .filter(isTenantModule)
    .filter((d) => d.spec?.application.kind !== "Info")
    .map((d) => `/admin/${d.spec?.application.plural ?? d.metadata.name}`)
    .sort()
    .join(",")
  return useMemo<SidebarSection[]>(() => {
    const sections: SidebarSection[] = [
      {
        title: "Administration",
        items: [
          {
            label: "Tenants",
            to: "/admin/tenants",
            icon: Users,
            alsoMatch: [`/admin/${infoPlural}`, "/admin/info"],
          },
          {
            label: "Modules",
            to: "/admin/modules",
            icon: ToyBrick,
            alsoMatch: modulePaths ? modulePaths.split(",") : undefined,
          },
          { label: "External IPs", to: "/admin/external-ips", icon: Globe },
        ],
      },
    ]
    if (canClusterUsage) {
      sections.push({
        title: "Capacity",
        items: [
          { label: "Cluster", to: "/admin/capacity/cluster", icon: Gauge },
          { label: "Nodes", to: "/admin/capacity/nodes", icon: Server },
          { label: "Storage", to: "/admin/capacity/storage", icon: HardDrive },
        ],
      })
    }
    if (canBackupClasses) {
      sections.push({
        title: "Backups",
        items: [
          { label: "Backup Classes", to: "/admin/backups/backupclasses", icon: Archive },
        ],
      })
    }
    return sections
  }, [canClusterUsage, canBackupClasses, infoPlural, modulePaths])
}
