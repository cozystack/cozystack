import type { TenantNamespace } from "@cozystack/types"
import { tenantDisplayName } from "./tenant-context.tsx"

const ANCESTOR_LABEL_PREFIX = "tenant.cozystack.io/"

export interface TenantTreeNode {
  tn: TenantNamespace
  /** Namespace of the nearest *visible* ancestor; undefined for forest roots. */
  parentNs?: string
  children: TenantTreeNode[]
  depth: number
}

export function ancestorNamespaces(tn: TenantNamespace): string[] {
  return Object.keys(tn.metadata.labels ?? {})
    .filter((k) => k.startsWith(ANCESTOR_LABEL_PREFIX))
    .map((k) => k.slice(ANCESTOR_LABEL_PREFIX.length))
    .filter((ns) => ns !== tn.metadata.name)
}

/**
 * Build a forest out of the visible TenantNamespaces. Each TN labels itself
 * with its full ancestor chain (`tenant.cozystack.io/<ancestor-ns>`), so the
 * parent is the deepest ancestor present in the visible set. A node whose
 * ancestors are all invisible (no access up the hierarchy) becomes a root.
 */
export function buildTenantForest(tns: TenantNamespace[]): TenantTreeNode[] {
  const visible = new Map(tns.map((tn) => [tn.metadata.name, tn]))
  const nodes = new Map<string, TenantTreeNode>(
    tns.map((tn) => [tn.metadata.name, { tn, children: [], depth: 0 }]),
  )

  const roots: TenantTreeNode[] = []
  for (const node of nodes.values()) {
    const candidates = ancestorNamespaces(node.tn).filter((ns) => visible.has(ns))
    // Deepest visible ancestor = the one with the most ancestors of its own.
    candidates.sort(
      (a, b) =>
        ancestorNamespaces(visible.get(b)!).length -
        ancestorNamespaces(visible.get(a)!).length,
    )
    const parentNs = candidates[0]
    if (parentNs) {
      node.parentNs = parentNs
      nodes.get(parentNs)!.children.push(node)
    } else {
      roots.push(node)
    }
  }

  const sortRec = (list: TenantTreeNode[], depth: number) => {
    list.sort((a, b) => a.tn.metadata.name.localeCompare(b.tn.metadata.name))
    for (const n of list) {
      n.depth = depth
      sortRec(n.children, depth + 1)
    }
  }
  sortRec(roots, 0)
  return roots
}

/** Depth-first row order, skipping the children of collapsed nodes. */
export function flattenTenantForest(
  roots: TenantTreeNode[],
  collapsed: Set<string>,
): TenantTreeNode[] {
  const out: TenantTreeNode[] = []
  const walk = (n: TenantTreeNode) => {
    out.push(n)
    if (!collapsed.has(n.tn.metadata.name)) n.children.forEach(walk)
  }
  roots.forEach(walk)
  return out
}

/**
 * Row label relative to the visible parent: `tenant-whmcs-crpjxhwm` under
 * `tenant-whmcs` shows as `crpjxhwm`. Forest roots keep their full display
 * name — there is no parent on screen to be relative to.
 */
export function relativeTenantName(node: TenantTreeNode): string {
  const ns = node.tn.metadata.name
  if (node.parentNs && ns.startsWith(`${node.parentNs}-`)) {
    return ns.slice(node.parentNs.length + 1)
  }
  return tenantDisplayName(node.tn)
}

/**
 * The namespace of a node's *real* parent, which is not always the one it
 * hangs under in the visible forest: a node whose real parent is inaccessible
 * is bridged onto its nearest visible ancestor instead. Tenant namespaces are
 * named hierarchically (`tenant-a-b` sits under `tenant-a`), so among the
 * node's own ancestor labels the real parent is the longest that prefixes it.
 * Returns undefined for the hierarchy root, which has no ancestors at all.
 */
export function realParentNamespace(tn: TenantNamespace): string | undefined {
  const ns = tn.metadata.name
  return ancestorNamespaces(tn)
    .filter((ancestor) => ns.startsWith(`${ancestor}-`))
    .sort((a, b) => b.length - a.length)[0]
}
