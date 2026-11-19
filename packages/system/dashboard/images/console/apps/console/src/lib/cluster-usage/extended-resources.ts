import { isExtendedResourceKey } from "./types.ts"
import type { Node } from "./types.ts"

/**
 * Returns the sorted, deduplicated set of extended-resource keys present
 * in any node's `status.capacity` across the cluster. Standard scheduler
 * resources (cpu, memory, ephemeral-storage, pods) and every hugepages-*
 * variant are filtered out — the rest is whatever the cluster exposes,
 * rendered verbatim. There is intentionally no vendor allow-list: a new
 * accelerator surfaces in the UI the moment a node exposing it joins.
 */
export function getExtendedResourceKeys(nodes: Node[]): string[] {
  const set = new Set<string>()
  for (const node of nodes) {
    const capacity = node.status?.capacity
    if (!capacity) continue
    for (const key of Object.keys(capacity)) {
      if (isExtendedResourceKey(key)) set.add(key)
    }
  }
  return [...set].sort()
}
