import { useMemo } from "react"
import { useNavigate } from "react-router"
import { useQueries } from "@tanstack/react-query"
import { useApplicationDefinitions, iconDataUrl, appDisplayName } from "../../lib/app-definitions"
import { useK8sList, useK8sClient } from "@cozystack/k8s-client"
import { APPS_GROUP, APPS_VERSION } from "@cozystack/types"
import { useTenantContext } from "../../lib/tenant-context"
import type { CommandItem, NavigationLevel } from "./types"

function matchesQuery(item: CommandItem, query: string): boolean {
  const q = query.toLowerCase()
  if (item.label.toLowerCase().includes(q)) return true
  if (item.description?.toLowerCase().includes(q)) return true
  if (item.keywords?.some((kw) => kw.toLowerCase().includes(q))) return true
  return false
}

function adIcon(icon?: string): React.ReactNode {
  if (!icon) return undefined
  return <img src={icon} alt="" className="h-4 w-4" />
}

interface UseCommandItemsResult {
  items: CommandItem[]
  isLoading: boolean
}

export function useCommandItems(
  query: string,
  level: NavigationLevel,
  navigate: (level: NavigationLevel) => void,
  close: () => void
): UseCommandItemsResult {
  const router = useNavigate()
  const client = useK8sClient()
  const { tenantNamespace } = useTenantContext()
  const { data: adList } = useApplicationDefinitions()

  const ads = adList?.items || []

  // For resource-level drill-down: fetch instances of one type
  const resourcePlural = level.type === "resource" ? level.plural : ""
  const { data: singleResourceData, isLoading: singleLoading } = useK8sList(
    {
      apiGroup: APPS_GROUP,
      apiVersion: APPS_VERSION,
      plural: resourcePlural,
      namespace: tenantNamespace ?? undefined,
    },
    { enabled: !!resourcePlural && !!tenantNamespace }
  )

  // For search mode: fetch all instances of all apps using useQueries
  const hasQuery = query.trim().length > 0
  const allInstancesQueries = useQueries({
    queries: (ads || []).map((ad) => {
      const plural = ad.spec?.application.plural ?? ""
      return {
        queryKey: [
          "k8s",
          APPS_GROUP,
          APPS_VERSION,
          plural,
          tenantNamespace ?? "",
          "",
          "",
        ] as const,
        queryFn: () =>
          client.list(APPS_GROUP, APPS_VERSION, plural, tenantNamespace ?? undefined),
        enabled: hasQuery && !!plural && !!tenantNamespace,
      }
    }),
  })

  // --- Root level items ---
  const rootItems = useMemo((): CommandItem[] => {
    const items: CommandItem[] = []

    for (const ad of ads) {
      const plural = ad.spec?.application.plural ?? ""
      if (!plural) continue
      const name = appDisplayName(ad)
      const icon = iconDataUrl(ad)

      items.push({
        id: `root-${plural}`,
        label: name,
        description: plural,
        icon: adIcon(icon),
        group: "Applications",
        drilldown: true,
        keywords: [plural],
        onSelect: () =>
          navigate({
            type: "resource",
            plural,
            label: name,
            icon,
          }),
      })
    }

    items.push({
      id: "root-marketplace",
      label: "Marketplace",
      description: "Browse available apps",
      group: "Quick Actions",
      keywords: ["marketplace", "catalog", "apps"],
      onSelect: () => {
        close()
        router("/marketplace")
      },
    })

    items.push({
      id: "root-console",
      label: "Console",
      description: "All instances overview",
      group: "Quick Actions",
      keywords: ["home", "dashboard", "console"],
      onSelect: () => {
        close()
        router("/console")
      },
    })

    return items
  }, [ads, navigate, router, close])

  // --- Resource level items (instances + create) ---
  const resourceItems = useMemo((): CommandItem[] => {
    if (level.type !== "resource") return []

    const items: CommandItem[] = []
    const instances = singleResourceData?.items ?? []

    for (const inst of instances) {
      items.push({
        id: `res-${inst.metadata.name}`,
        label: inst.metadata.name,
        drilldown: true,
        icon: adIcon(level.icon),
        onSelect: () =>
          navigate({
            type: "instance",
            plural: level.plural,
            instance: inst,
            label: inst.metadata.name,
            resourceLabel: level.label,
            icon: level.icon,
          }),
      })
    }

    items.push({
      id: "res-create",
      label: `Create new ${level.label}`,
      group: "Actions",
      keywords: ["create", "new", "deploy"],
      onSelect: () => {
        close()
        router(`/console/${level.plural}/create`)
      },
    })

    return items
  }, [level, singleResourceData, router, navigate, close])

  // --- Instance level items (actions) ---
  const instanceItems = useMemo((): CommandItem[] => {
    if (level.type !== "instance") return []

    const items: CommandItem[] = []
    const { plural, instance } = level

    items.push({
      id: "inst-open",
      label: "Open detail page",
      onSelect: () => {
        close()
        router(`/console/${plural}/${instance.metadata.name}`)
      },
    })

    items.push({
      id: "inst-edit",
      label: "Edit",
      onSelect: () => {
        close()
        router(`/console/${plural}/${instance.metadata.name}/edit`)
      },
    })

    items.push({
      id: "inst-yaml",
      label: "View YAML",
      onSelect: () => {
        close()
        router(`/console/${plural}/${instance.metadata.name}?tab=yaml`)
      },
    })

    return items
  }, [level, router, close])

  // --- Flat search across everything ---
  const searchItems = useMemo((): CommandItem[] => {
    const items: CommandItem[] = []

    // All resource types
    for (const ad of ads) {
      const plural = ad.spec?.application.plural ?? ""
      if (!plural) continue
      const name = appDisplayName(ad)
      const icon = iconDataUrl(ad)

      items.push({
        id: `search-nav-${plural}`,
        label: name,
        description: plural,
        icon: adIcon(icon),
        group: "Navigate",
        drilldown: true,
        keywords: [plural],
        onSelect: () =>
          navigate({
            type: "resource",
            plural,
            label: name,
            icon,
          }),
      })

      items.push({
        id: `search-create-${plural}`,
        label: `Create ${name}`,
        description: plural,
        icon: adIcon(icon),
        group: "Create New",
        keywords: ["create", "new", "deploy", plural],
        onSelect: () => {
          close()
          router(`/console/${plural}/create`)
        },
      })
    }

    // All instances from all apps
    for (let i = 0; i < ads.length; i++) {
      const ad = ads[i]
      const plural = ad.spec?.application.plural ?? ""
      const name = appDisplayName(ad)
      const icon = iconDataUrl(ad)
      const queryResult = allInstancesQueries[i]
      const instances = queryResult?.data?.items ?? []

      for (const inst of instances) {
        const instance = inst as any
        items.push({
          id: `search-inst-${plural}-${instance.metadata.name}`,
          label: instance.metadata.name,
          description: name,
          icon: adIcon(icon),
          group: "Instances",
          drilldown: true,
          keywords: [instance.metadata.name, plural, name],
          onSelect: () =>
            navigate({
              type: "instance",
              plural,
              instance,
              label: instance.metadata.name,
              resourceLabel: name,
              icon,
            }),
        })
      }
    }

    // Static actions
    items.push({
      id: "search-marketplace",
      label: "Marketplace",
      description: "Browse available apps",
      group: "Quick Actions",
      keywords: ["marketplace", "catalog", "apps"],
      onSelect: () => {
        close()
        router("/marketplace")
      },
    })

    items.push({
      id: "search-console",
      label: "Console",
      description: "All instances overview",
      group: "Quick Actions",
      keywords: ["home", "dashboard", "console"],
      onSelect: () => {
        close()
        router("/console")
      },
    })

    return items
  }, [ads, router, navigate, close, allInstancesQueries])

  // --- Select items based on mode ---
  const currentItems = useMemo(() => {
    if (hasQuery) {
      return searchItems.filter((item) => matchesQuery(item, query))
    }
    switch (level.type) {
      case "root":
        return rootItems
      case "resource":
        return resourceItems
      case "instance":
        return instanceItems
    }
  }, [hasQuery, query, level.type, searchItems, rootItems, resourceItems, instanceItems])

  const searchLoading = hasQuery && allInstancesQueries.some((q) => q.isLoading)
  const isLoading = (level.type === "resource" && singleLoading) || searchLoading

  return { items: currentItems, isLoading }
}
