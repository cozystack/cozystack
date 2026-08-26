import {
  useK8sList,
  useK8sCreate,
  useK8sDelete,
  type ResourceRef,
} from "@cozystack/k8s-client"
import { CORE_GROUP, CORE_VERSION, type Tap } from "@cozystack/types"

// Community-tap name prefix; must match internal/marketplace/tapconst.Prefix.
export const COMMUNITY_PREFIX = "community."

// Cluster-scoped: no namespace key.
const TAPS_REF: ResourceRef = {
  apiGroup: CORE_GROUP,
  apiVersion: CORE_VERSION,
  plural: "taps",
}

export function useTaps() {
  return useK8sList<Tap>(TAPS_REF)
}

export function useConnectTap() {
  return useK8sCreate<Tap>(TAPS_REF)
}

export function useDisconnectTap() {
  return useK8sDelete(TAPS_REF)
}

/**
 * deriveTapName mirrors the server's naming (community.<org>.<repo>) from an
 * oci:// URL so the create request carries the name the server will assign,
 * keeping the request and the created object in agreement.
 */
export function deriveTapName(url: string): string {
  let body = url.replace(/^oci:\/\//, "")
  const at = body.indexOf("@")
  if (at >= 0) body = body.slice(0, at)
  const lastSlash = body.lastIndexOf("/")
  const lastColon = body.lastIndexOf(":")
  if (lastColon > lastSlash) body = body.slice(0, lastColon)
  const segs = body.split("/").filter(Boolean)
  const repo = segs[segs.length - 1] ?? ""
  const org = segs.length >= 3 ? segs[segs.length - 2] : ""
  return org ? `${COMMUNITY_PREFIX}${org}.${repo}` : `${COMMUNITY_PREFIX}${repo}`
}
