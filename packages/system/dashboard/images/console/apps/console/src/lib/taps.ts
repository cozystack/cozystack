import {
  useK8sList,
  useK8sCreate,
  useK8sDelete,
  type ResourceRef,
} from "@cozystack/k8s-client"
import { CORE_GROUP, CORE_VERSION, type Tap } from "@cozystack/types"

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
 * deriveTapName mirrors the server's Flux source naming (tap-<org>-<repo>) from
 * an oci:// URL, so the create request carries the name the server assigns to
 * the tap's source object. A tapped repository keeps its own declared
 * PackageSource name(s), materialized later, so the source is the tap's stable
 * identity rather than a name derived from the artifact.
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
  const base = org ? `${org}-${repo}` : repo
  return "tap-" + base.toLowerCase().replace(/[^a-z0-9-]+/g, "-")
}
