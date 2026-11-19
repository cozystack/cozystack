import {
  findImmutablePaths,
  overlayImmutable,
  type ImmutablePath,
} from "./immutable-paths.ts"

/**
 * Build the spec to PUT during an edit, overlaying immutable values from
 * the original spec so that the API server sees no delta on paths whose
 * schema carries `x-kubernetes-validations: [{rule: "self == oldSelf"}]`.
 *
 * Defence-in-depth: even if the form's read-only state is bypassed (via
 * the YAML editor, devtools, or a UI bug) the outgoing body still
 * matches the persisted spec on those paths.
 *
 * Edge cases:
 *  - When `original` is null/undefined there is nothing meaningful to
 *    overlay (typically the resource hasn't loaded yet, or the persisted
 *    spec was empty). We return the submitted value unchanged rather than
 *    blanking immutable fields with undefined.
 *  - When the schema string cannot be parsed we log a warning so the
 *    misconfiguration is at least visible in DevTools, then return the
 *    submitted value unchanged.
 */
export function prepareUpdateSpec<T>(
  submitted: T,
  original: T | null | undefined,
  openAPISchema: string,
): T {
  if (original === undefined || original === null) {
    return cloneShallowAsDeep(submitted)
  }
  let paths: ImmutablePath[]
  try {
    paths = findImmutablePaths(JSON.parse(openAPISchema))
  } catch {
    console.warn(
      "prepareUpdateSpec: failed to parse openAPISchema; " +
        "skipping immutable-field overlay.",
    )
    paths = []
  }
  return overlayImmutable(submitted, original, paths)
}

function cloneShallowAsDeep<T>(value: T): T {
  if (value === null || value === undefined) return value
  return JSON.parse(JSON.stringify(value)) as T
}
