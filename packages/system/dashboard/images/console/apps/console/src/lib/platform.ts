/**
 * Whether the paste chord on this machine is Cmd rather than Ctrl.
 *
 * Read at the moment a key is handled rather than through a hook: the console
 * tabs hold a live socket, and a state change that lands after the first
 * render would tear the session down and rebuild it.
 */
export function pastesWithMeta(): boolean {
  if (typeof navigator === "undefined") return false
  const platform = navigator.platform ?? ""
  return /mac|iphone|ipad|ipod/i.test(platform)
}
