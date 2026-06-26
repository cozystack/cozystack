/**
 * Discovery and enforcement of Kubernetes-style immutability rules
 * (`x-kubernetes-validations: [{rule: "self == oldSelf"}]`) on OpenAPI/JSON
 * schemas served by Cozystack ApplicationDefinitions and CRDs.
 *
 * The UI strips this extension before handing the schema to AJV (which has no
 * CEL evaluator); the paths it announces are used to (a) mark form fields as
 * read-only in edit mode and (b) overlay the original values into PUT bodies
 * so the API server never observes a change on those paths.
 */

export type ImmutablePath = readonly string[]

export const IMMUTABLE_HELP_TEXT =
  "This field cannot be changed after creation."

const IMMUTABILITY_RULE = "self == oldSelf"

const isPlainObject = (v: unknown): v is Record<string, unknown> =>
  v !== null && typeof v === "object" && !Array.isArray(v)

const hasImmutabilityRule = (node: unknown): boolean => {
  if (!isPlainObject(node)) return false
  const validations = node["x-kubernetes-validations"]
  if (!Array.isArray(validations)) return false
  return validations.some(
    (v) => isPlainObject(v) && v.rule === IMMUTABILITY_RULE,
  )
}

const branchHasImmutability = (node: unknown): boolean => {
  if (!isPlainObject(node)) return false
  if (hasImmutabilityRule(node)) return true
  for (const key of ["oneOf", "anyOf", "allOf"] as const) {
    const branches = node[key]
    if (Array.isArray(branches) && branches.some(branchHasImmutability)) {
      return true
    }
  }
  return false
}

export function findImmutablePaths(schema: unknown): ImmutablePath[] {
  const out: string[][] = []
  walk(schema, [], out)
  return out
}

function walk(node: unknown, path: string[], out: string[][]): void {
  if (!isPlainObject(node)) return

  if (branchHasImmutability(node)) {
    out.push([...path])
    return
  }

  const properties = node.properties
  if (isPlainObject(properties)) {
    for (const [key, value] of Object.entries(properties)) {
      walk(value, [...path, key], out)
    }
  }

  const items = node.items
  if (isPlainObject(items)) {
    walk(items, [...path, "*"], out)
  }

  const additional = node.additionalProperties
  if (isPlainObject(additional)) {
    walk(additional, [...path, "*"], out)
  }
}

export function overlayImmutable<T>(
  submitted: T,
  original: T,
  paths: readonly ImmutablePath[],
): T {
  if (paths.some((p) => p.length === 0)) {
    // The schema flags the whole subtree as immutable; whatever the user
    // changed in the form is dropped on the floor. That's almost always a
    // schema-authoring mistake (rule belongs on a property, not the root)
    // so make it visible in the console.
    console.warn(
      "overlayImmutable: a root-level immutable path was supplied; " +
        "the submitted value is replaced wholesale by the original.",
    )
    return deepClone(original)
  }
  if (paths.some((p) => p[0] === "*")) {
    // A leading wildcard would land at the very top of the structure and
    // overlay nothing useful (the root is an array or map, not an object
    // we can deepen into). Mirror the root-immutable handling: warn and
    // hand back a clone of original.
    console.warn(
      "overlayImmutable: a top-level wildcard immutable path was supplied; " +
        "the submitted value is replaced wholesale by the original.",
    )
    return deepClone(original)
  }
  const next = deepClone(submitted)
  for (const path of paths) {
    if (wildcardArrayLengthChanged(next, original, path)) {
      // Index-aligned overlay corrupts the user's deletion/insertion when
      // a per-element-nested-immutable path crosses an array whose length
      // changed. The UI greys those fields out, so the only ways to land
      // here are the YAML editor or devtools — the API server will reject
      // a per-element mutation via CEL. Skip the overlay (warn for diag)
      // rather than silently re-anchor source values to user indices.
      console.warn(
        "overlayImmutable: array length changed at wildcard segment of",
        path.join("."),
        "— skipping overlay for this path; admission will enforce.",
      )
      continue
    }
    overlayPath(next, original, path, 0)
  }
  return next
}

function wildcardArrayLengthChanged(
  submitted: unknown,
  original: unknown,
  path: ImmutablePath,
): boolean {
  let subCur: unknown = submitted
  let origCur: unknown = original
  for (let i = 0; i < path.length; i++) {
    const seg = path[i]
    if (seg === "*") {
      // Whole-array immutability (*-last) is fine to overlay even when the
      // user changed the length — the semantics is "freeze, replace from
      // source", and that's exactly what the *-last branch does.
      const isLast = i === path.length - 1
      if (isLast) return false
      // Per-element-nested immutability (*-not-last) is the case that can
      // silently corrupt on SHRINK (we'd re-anchor source values onto the
      // user's surviving indices, which may belong to a different element
      // than the user thought they kept). On grow the shared indices stay
      // put and the new entries past source.length keep the user's values,
      // so growth is safe to overlay.
      if (Array.isArray(subCur) && Array.isArray(origCur)) {
        return subCur.length < origCur.length
      }
      return false
    }
    if (!isPlainObject(subCur) || !isPlainObject(origCur)) return false
    subCur = (subCur as Record<string, unknown>)[seg]
    origCur = (origCur as Record<string, unknown>)[seg]
  }
  return false
}

function overlayPath(
  target: unknown,
  source: unknown,
  path: ImmutablePath,
  depth: number,
): unknown {
  if (depth === path.length) {
    // End of the path: copy the source value. If source has nothing at
    // this position keep what the user submitted (avoids blanking fields
    // that the persisted spec doesn't echo back).
    if (source === undefined) return target
    return deepClone(source)
  }
  const seg = path[depth]
  if (seg === "*") {
    return overlayWildcard(target, source, path, depth)
  }
  const sourceObj = isPlainObject(source) ? source : undefined
  const sourceVal = sourceObj ? sourceObj[seg] : undefined
  const targetObj = isPlainObject(target)
    ? (target as Record<string, unknown>)
    : null
  if (!targetObj) return target
  if (
    sourceVal === undefined &&
    !Object.prototype.hasOwnProperty.call(targetObj, seg)
  ) {
    // Neither side has anything at this path; nothing to overlay or insert.
    return targetObj
  }
  targetObj[seg] = overlayPath(targetObj[seg], sourceVal, path, depth + 1)
  return targetObj
}

/**
 * A wildcard segment represents either "every element of an array" or
 * "every entry of an object map" (additionalProperties). The walker emits
 * the same "*" symbol for both because the schema decides at runtime
 * which kind of node sits at the path; we dispatch on the actual value
 * shape here.
 *
 * Whole-collection immutable (*-last): replace target with a deep clone
 * of source, freezing the collection wholesale. Granular immutable
 * (*-not-last): iterate the user's collection (so adds and removes
 * survive) and overlay the nested immutable subfield from source on
 * shared elements/keys.
 */
function overlayWildcard(
  target: unknown,
  source: unknown,
  path: ImmutablePath,
  depth: number,
): unknown {
  const isLast = depth === path.length - 1

  if (Array.isArray(source)) {
    if (isLast) return deepClone(source)
    const targetArr = Array.isArray(target) ? target : []
    const out: unknown[] = []
    for (let i = 0; i < targetArr.length; i++) {
      if (i < source.length) {
        out[i] = overlayPath(targetArr[i], source[i], path, depth + 1)
      } else {
        out[i] = deepClone(targetArr[i])
      }
    }
    return out
  }

  if (isPlainObject(source)) {
    // For additionalProperties (object maps) we freeze the whole map
    // regardless of whether * is last or not. The UI marks the field
    // ui:disabled and hides Add/Remove, so this overlay aligns the
    // defence-in-depth (YAML editor / devtools) with what the form
    // already enforces — added keys are dropped, removed keys are
    // reinstated, every value is reset to source.
    return deepClone(source)
  }

  return target
}

function deepClone<T>(value: T): T {
  if (value === null || value === undefined) return value
  if (Array.isArray(value)) return value.map(deepClone) as unknown as T
  if (typeof value === "object") {
    const out: Record<string, unknown> = {}
    for (const [k, v] of Object.entries(value as Record<string, unknown>)) {
      out[k] = deepClone(v)
    }
    return out as unknown as T
  }
  return value
}
