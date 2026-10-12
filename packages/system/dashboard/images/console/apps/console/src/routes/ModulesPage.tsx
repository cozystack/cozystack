import { useMemo, useState, type ReactNode } from "react"
import { useNavigate } from "react-router"
import { ArrowUpFromLine, ChevronDown, ChevronRight } from "lucide-react"
import { Section, Spinner, StatusBadge } from "@cozystack/ui"
import {
  useK8sList,
  type K8sResource,
} from "@cozystack/k8s-client"
import type {
  ApplicationDefinition,
  ApplicationInstance,
  TenantNamespace,
} from "@cozystack/types"
import {
  iconDataUrl,
  isTenantModule,
  useApplicationDefinitions,
} from "../lib/app-definitions.ts"
import { useResourceBasePath } from "../lib/portal.ts"
import { useTenantContext } from "../lib/tenant-context.tsx"
import {
  buildTenantForest,
  relativeTenantName,
  type TenantTreeNode,
} from "../lib/tenant-tree.ts"
import { TENANT_NAMESPACE_PREFIX } from "../lib/constants.ts"
import { humanizeKind } from "../lib/humanize.ts"
import { readyCondition } from "../lib/status.ts"

const TENANT_MODULES_REF = {
  apiGroup: "core.cozystack.io",
  apiVersion: "v1alpha1",
  plural: "tenantmodules",
}

const APP_KIND_LABEL = "apps.cozystack.io/application.kind"
const PROVIDER_LABEL_PREFIX = "namespace.cozystack.io/"

/**
 * Administration → Modules: the tenant hierarchy as a connector-line tree —
 * each tenant's name is followed by its module chips, with child tenants
 * hanging off a trunk that drops from the parent's name. Per tenant the
 * `namespace.cozystack.io/<module>` label on its TenantNamespace names the
 * providing namespace: the tenant itself → a local chip; an inaccessible
 * ancestor → an inherited chip (blue, up arrow). When the provider is on
 * screen its own chip already shows the module, so no inherited chip repeats
 * it. Tenants whose whole subtree has no modules collapse into a
 * "+ N tenants" pseudo-branch. Clicking a chip opens the module in whichever
 * tenant runs it.
 */
export function ModulesPage() {
  const { data: defs, isLoading: defsLoading } = useApplicationDefinitions()
  const { selectTenant } = useTenantContext()
  const basePath = useResourceBasePath()
  const navigate = useNavigate()
  const [collapsed, setCollapsed] = useState<Set<string>>(new Set())

  const toggleCollapse = (ns: string) => {
    setCollapsed((prev) => {
      const next = new Set(prev)
      if (next.has(ns)) next.delete(ns)
      else next.add(ns)
      return next
    })
  }

  const { data: tnList, isLoading: tnLoading } = useK8sList<TenantNamespace>({
    apiGroup: "core.cozystack.io",
    apiVersion: "v1alpha1",
    plural: "tenantnamespaces",
  })

  // Cluster-wide TenantModule registry — carries the instance name per
  // (namespace, kind) for chip navigation targets.
  const { data: tmList } = useK8sList<K8sResource>(TENANT_MODULES_REF)

  const modules = useMemo(
    () =>
      (defs?.items ?? [])
        .filter(isTenantModule)
        // Info is installed everywhere by default — pure noise here; it has
        // its own row action on the Tenants page.
        .filter((ad) => ad.spec?.application.kind !== "Info")
        .sort((a, b) =>
          (a.spec?.application.kind ?? "").localeCompare(
            b.spec?.application.kind ?? "",
          ),
        ),
    [defs],
  )

  // TenantModules carry the instance name and a conditions-based status in
  // the same shape as application instances.
  const tms = useMemo(() => {
    const map = new Map<string, K8sResource>()
    for (const tm of tmList?.items ?? []) {
      const kind = tm.metadata.labels?.[APP_KIND_LABEL]
      const ns = tm.metadata.namespace
      if (kind && ns) map.set(`${ns}/${kind}`, tm)
    }
    return map
  }, [tmList])

  const roots = useMemo(
    () => buildTenantForest(tnList?.items ?? []),
    [tnList],
  )

  const visibleNs = useMemo(
    () => new Set((tnList?.items ?? []).map((tn) => tn.metadata.name)),
    [tnList],
  )

  // Tenants with no modules anywhere in their subtree are hidden entirely; a
  // chipless tenant stays only as the container of chipful descendants.
  const visibleRoots = useMemo(
    () => roots.filter((r) => subtreeHasModules(r, modules, tms, visibleNs)),
    [roots, modules, tms, visibleNs],
  )

  // Open the module instance in the tenant that runs it: the area's own
  // tenant for local chips, the providing ancestor for inherited ones.
  const openModule = (providerNs: string, ad: ApplicationDefinition) => {
    const kind = ad.spec?.application.kind ?? ad.metadata.name
    const plural = ad.spec?.application.plural ?? ad.metadata.name
    const name =
      tms.get(`${providerNs}/${kind}`)?.metadata.name ?? kind.toLowerCase()
    selectTenant(providerNs.slice(TENANT_NAMESPACE_PREFIX.length))
    navigate(`${basePath}/${plural}/${name}`)
  }

  if (defsLoading || tnLoading) {
    return (
      <div className="flex items-center gap-2 p-6 text-sm text-slate-500">
        <Spinner /> Loading modules…
      </div>
    )
  }

  return (
    <div className="p-6">
      <div className="mb-5">
        <h1 className="text-xl font-semibold text-slate-900">Modules</h1>
        <p className="mt-0.5 text-sm text-slate-500">
          Modules per tenant across everything you can access.
        </p>
      </div>

      {visibleRoots.length === 0 || modules.length === 0 ? (
        <Section>
          <p className="py-6 text-center text-sm text-slate-500">No modules.</p>
        </Section>
      ) : (
        <div className="space-y-6">
          {visibleRoots.map((node) => (
            <TenantSubtree
              key={node.tn.metadata.name}
              node={node}
              modules={modules}
              tms={tms}
              visibleNs={visibleNs}
              collapsed={collapsed}
              onToggle={toggleCollapse}
              onOpen={openModule}
            />
          ))}
        </div>
      )}
    </div>
  )
}

interface ModuleChip {
  ad: ApplicationDefinition
  kind: string
  provider: string
  inherited: boolean
  ready: ReturnType<typeof readyCondition>
}

function moduleChips(
  node: TenantTreeNode,
  modules: ApplicationDefinition[],
  tms: Map<string, K8sResource>,
  visibleNs: Set<string>,
): ModuleChip[] {
  const ns = node.tn.metadata.name
  const labels = node.tn.metadata.labels ?? {}

  return modules.flatMap((ad) => {
    const kind = ad.spec?.application.kind ?? ad.metadata.name
    // Both lists are live watches, and on a module toggle the TenantModule and
    // the provider label can land in either order — show the chip on whichever
    // signal arrives first.
    const provider =
      labels[PROVIDER_LABEL_PREFIX + kind.toLowerCase()] ||
      (tms.has(`${ns}/${kind}`) ? ns : undefined)
    if (!provider) return []
    const inherited = provider !== ns
    // An inherited chip duplicates the provider's own chip whenever that
    // tenant is on screen — show it only when the provider is not accessible.
    if (inherited && visibleNs.has(provider)) return []
    const ready = readyCondition(
      (tms.get(`${provider}/${kind}`) ?? {}) as ApplicationInstance,
    )
    return [{ ad, kind, provider, inherited, ready }]
  })
}

function subtreeHasModules(
  node: TenantTreeNode,
  modules: ApplicationDefinition[],
  tms: Map<string, K8sResource>,
  visibleNs: Set<string>,
): boolean {
  return (
    moduleChips(node, modules, tms, visibleNs).length > 0 ||
    node.children.some((c) => subtreeHasModules(c, modules, tms, visibleNs))
  )
}

/**
 * One branch of the tree: a continuous vertical trunk drops out of the
 * parent's area (or continues from the previous sibling's branch) and ends in
 * a horizontal stub pointing at this row. Non-last branches keep the trunk
 * running through their whole subtree so it reaches the next sibling.
 */
function Branch({
  isLast,
  children,
}: {
  isLast: boolean
  children: ReactNode
}) {
  return (
    <div className="relative pl-10">
      {/* Rounded elbow: drops from the parent above and curves into the row. */}
      <span className="absolute left-2 top-0 h-[34px] w-4 rounded-bl border-b border-l border-slate-300" />
      {/* Trunk continues through non-last branches to reach the next sibling. */}
      {!isLast && (
        <span className="absolute left-2 top-0 h-full w-px bg-slate-300" />
      )}
      <div className="pt-6">{children}</div>
    </div>
  )
}

function TenantSubtree({
  node,
  modules,
  tms,
  visibleNs,
  collapsed,
  onToggle,
  onOpen,
}: {
  node: TenantTreeNode
  modules: ApplicationDefinition[]
  tms: Map<string, K8sResource>
  visibleNs: Set<string>
  collapsed: Set<string>
  onToggle: (ns: string) => void
  onOpen: (providerNs: string, ad: ApplicationDefinition) => void
}) {
  const ns = node.tn.metadata.name
  const chips = moduleChips(node, modules, tms, visibleNs)
  const shown = node.children.filter((c) =>
    subtreeHasModules(c, modules, tms, visibleNs),
  )
  const hidden = node.children.length - shown.length
  const hasBranches = shown.length > 0 || hidden > 0
  const isCollapsed = collapsed.has(ns)

  return (
    <div>
      <div className="flex items-center gap-1.5">
        <p className="truncate text-[15px] font-medium text-slate-900">
          {relativeTenantName(node)}
        </p>
        {(chips.length > 0 || hasBranches) && (
          <button
            type="button"
            onClick={() => onToggle(ns)}
            title={isCollapsed ? "Expand" : "Collapse"}
            className="shrink-0 rounded p-0.5 text-slate-400 hover:bg-slate-100 hover:text-slate-700"
          >
            {isCollapsed ? (
              <ChevronRight className="size-4" />
            ) : (
              <ChevronDown className="size-4" />
            )}
          </button>
        )}
      </div>
      {!isCollapsed && (
        <>
      {chips.length > 0 && (
        // The trunk starts right under the parent's name and runs past the
        // chips down to the child branches.
        <div className="relative mt-6">
          {hasBranches && (
            // Reaches up through the name→chips gap so the trunk still plugs
            // into the parent's name.
            <span className="absolute -top-6 left-2 h-[calc(100%+1.5rem)] w-px bg-slate-300" />
          )}
          {/* Chips step aside for the trunk only when one passes; a childless
              tenant keeps them flush with its own name. */}
          <div className={`flex flex-wrap gap-2 ${hasBranches ? "pl-7" : ""}`}>
          {chips.map(({ ad, kind, provider, inherited, ready }) => {
            const icon = iconDataUrl(ad)
            // An inherited chip is shown only when its providing tenant is NOT
            // visible to the user, so there is nothing to open: the instance
            // GET would be denied and switching the active tenant to a
            // namespace outside the visible set bounces the selector to an
            // arbitrary fallback. It stays as a read-only badge that says where
            // the module comes from.
            const Chip = inherited ? "div" : "button"
            return (
              <Chip
                key={ad.metadata.name}
                {...(inherited
                  ? {}
                  : { type: "button" as const, onClick: () => onOpen(provider, ad) })}
                title={
                  inherited
                    ? `Inherited from tenant ${provider.slice(TENANT_NAMESPACE_PREFIX.length)}, which you cannot open`
                    : `${humanizeKind(kind)} runs in ${relativeTenantName(node)}`
                }
                className={`group flex w-80 items-center justify-between gap-3 rounded-lg border px-4 py-3 ${
                  inherited
                    ? "cursor-default border-blue-200 bg-blue-50"
                    : "border-slate-200 bg-white transition-shadow hover:shadow-sm"
                }`}
              >
                <div className="flex min-w-0 items-center gap-3">
                  <div className="size-9 shrink-0 overflow-hidden rounded-md bg-slate-100">
                    {icon ? (
                      <img src={icon} alt="" className="h-full w-full" />
                    ) : null}
                  </div>
                  <div className="min-w-0 text-left">
                    <p className="truncate text-sm font-semibold text-slate-900 group-hover:text-blue-700">
                      {humanizeKind(kind)}
                    </p>
                    <p
                      className={`truncate font-mono text-[11px] ${inherited ? "text-blue-600" : "text-slate-400"}`}
                    >
                      {provider}
                    </p>
                  </div>
                </div>
                <div className="flex shrink-0 items-center gap-2">
                  {ready ? (
                    <StatusBadge tone={ready.status === "True" ? "ok" : "warn"}>
                      {ready.status === "True" ? "Ready" : (ready.reason ?? "NotReady")}
                    </StatusBadge>
                  ) : (
                    <StatusBadge tone="muted">Unknown</StatusBadge>
                  )}
                  {inherited && (
                    <ArrowUpFromLine className="size-3 text-blue-600" />
                  )}
                </div>
              </Chip>
            )
          })}
          </div>
        </div>
      )}
          {hasBranches && (
            <div>
              {shown.map((child, i) => (
                <Branch
                  key={child.tn.metadata.name}
                  isLast={i === shown.length - 1 && hidden === 0}
                >
                  <TenantSubtree
                    node={child}
                    modules={modules}
                    tms={tms}
                    visibleNs={visibleNs}
                    collapsed={collapsed}
                    onToggle={onToggle}
                    onOpen={onOpen}
                  />
                </Branch>
              ))}
              {hidden > 0 && (
                <Branch isLast>
                  <span className="text-xs text-slate-400">
                    + {hidden} tenant{hidden > 1 ? "s" : ""}
                  </span>
                </Branch>
              )}
            </div>
          )}
        </>
      )}
    </div>
  )
}
