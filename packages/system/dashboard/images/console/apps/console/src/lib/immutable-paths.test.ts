import { describe, it, expect, vi } from "vitest"
import { findImmutablePaths, overlayImmutable } from "./immutable-paths.ts"

const cel = {
  "x-kubernetes-validations": [
    { rule: "self == oldSelf", message: "immutable" },
  ],
}

describe("findImmutablePaths", () => {
  it("returns empty array for non-object input", () => {
    expect(findImmutablePaths(null)).toEqual([])
    expect(findImmutablePaths(undefined)).toEqual([])
    expect(findImmutablePaths("string")).toEqual([])
    expect(findImmutablePaths(42)).toEqual([])
  })

  it("returns empty array when no validations are present", () => {
    expect(
      findImmutablePaths({
        type: "object",
        properties: { foo: { type: "string" } },
      }),
    ).toEqual([])
  })

  it("ignores validations whose rule is not self == oldSelf", () => {
    expect(
      findImmutablePaths({
        properties: {
          foo: {
            type: "string",
            "x-kubernetes-validations": [{ rule: "self.size() > 0" }],
          },
        },
      }),
    ).toEqual([])
  })

  it("finds a top-level immutable property", () => {
    const schema = {
      properties: {
        storageClass: { type: "string", ...cel },
        size: { type: "string" },
      },
    }
    expect(findImmutablePaths(schema)).toEqual([["storageClass"]])
  })

  it("finds nested immutable properties", () => {
    const schema = {
      properties: {
        spec: {
          properties: {
            backup: {
              properties: {
                storageClass: { type: "string", ...cel },
              },
            },
          },
        },
      },
    }
    expect(findImmutablePaths(schema)).toEqual([
      ["spec", "backup", "storageClass"],
    ])
  })

  it("uses \"*\" for array items", () => {
    const schema = {
      properties: {
        disks: {
          type: "array",
          items: { type: "string", ...cel },
        },
      },
    }
    expect(findImmutablePaths(schema)).toEqual([["disks", "*"]])
  })

  it("descends into array items to find nested immutable fields", () => {
    const schema = {
      properties: {
        disks: {
          type: "array",
          items: {
            type: "object",
            properties: {
              name: { type: "string", ...cel },
              size: { type: "string" },
            },
          },
        },
      },
    }
    expect(findImmutablePaths(schema)).toEqual([["disks", "*", "name"]])
  })

  it("uses \"*\" for additionalProperties object maps", () => {
    const schema = {
      properties: {
        labels: {
          type: "object",
          additionalProperties: { type: "string", ...cel },
        },
      },
    }
    expect(findImmutablePaths(schema)).toEqual([["labels", "*"]])
  })

  it("treats parent as immutable when a oneOf branch carries the rule", () => {
    const schema = {
      properties: {
        size: {
          oneOf: [{ type: "integer" }, { type: "string", ...cel }],
        },
      },
    }
    expect(findImmutablePaths(schema)).toEqual([["size"]])
  })

  it("treats parent as immutable when anyOf/allOf carries the rule", () => {
    expect(
      findImmutablePaths({
        properties: {
          foo: { anyOf: [{ ...cel }] },
        },
      }),
    ).toEqual([["foo"]])
    expect(
      findImmutablePaths({
        properties: {
          bar: { allOf: [{ ...cel }] },
        },
      }),
    ).toEqual([["bar"]])
  })

  it("handles root-level immutable schema (no properties)", () => {
    expect(findImmutablePaths(cel)).toEqual([[]])
  })

  it("does NOT walk into properties beneath oneOf/anyOf/allOf branches (pinned current behaviour)", () => {
    // Tracked in cozystack/cozystack-ui#8.
    // The walker recognises *parent* immutability when any branch carries
    // the rule, but it does not descend into a branch's `properties` to
    // discover per-field immutability inside the branch. CRDs that use
    // allOf composition for property merging are not covered.
    // Pinned as current behaviour; revisit if a real schema needs it.
    const schema = {
      type: "object",
      allOf: [
        {
          properties: {
            storageClass: { type: "string", ...cel },
          },
        },
      ],
    }
    expect(findImmutablePaths(schema)).toEqual([])
  })

  it("collects multiple immutable paths in deterministic order", () => {
    const schema = {
      properties: {
        a: { type: "string", ...cel },
        b: {
          properties: {
            c: { type: "string", ...cel },
          },
        },
      },
    }
    expect(findImmutablePaths(schema)).toEqual([["a"], ["b", "c"]])
  })
})

describe("overlayImmutable", () => {
  it("returns a deep clone when paths is empty", () => {
    const submitted = { foo: "x", arr: [1, 2] }
    const original = { foo: "y", arr: [9] }
    const result = overlayImmutable(submitted, original, [])
    expect(result).toEqual(submitted)
    expect(result).not.toBe(submitted)
    expect((result as { arr: number[] }).arr).not.toBe(submitted.arr)
  })

  it("overlays a top-level immutable field", () => {
    const submitted = { storageClass: "fast", size: "10Gi" }
    const original = { storageClass: "slow", size: "5Gi" }
    expect(
      overlayImmutable(submitted, original, [["storageClass"]]),
    ).toEqual({ storageClass: "slow", size: "10Gi" })
  })

  it("preserves siblings when overlaying nested fields", () => {
    const submitted = {
      spec: { backup: { storageClass: "fast", schedule: "*/5" } },
    }
    const original = {
      spec: { backup: { storageClass: "slow", schedule: "*/1" } },
    }
    expect(
      overlayImmutable(submitted, original, [
        ["spec", "backup", "storageClass"],
      ]),
    ).toEqual({
      spec: { backup: { storageClass: "slow", schedule: "*/5" } },
    })
  })

  it("inserts the immutable field if it is missing from submitted but present in original", () => {
    const submitted = { size: "10Gi" } as Record<string, unknown>
    const original = { size: "5Gi", storageClass: "slow" }
    expect(
      overlayImmutable(submitted, original, [["storageClass"]]),
    ).toEqual({ size: "10Gi", storageClass: "slow" })
  })

  it("keeps the submitted value when the immutable field is absent from original", () => {
    // Persisted spec doesn't have the field (server-default, status echo gap,
    // freshly-loaded resource). overlay must not blank user's input with undefined.
    expect(
      overlayImmutable(
        { storageClass: "fast", size: "10Gi" },
        { size: "5Gi" } as Record<string, unknown>,
        [["storageClass"]],
      ),
    ).toEqual({ storageClass: "fast", size: "10Gi" })
  })

  it("aligns array length to original when array element path is immutable", () => {
    const submitted = { disks: ["x", "y", "z"] }
    const original = { disks: ["a", "b"] }
    expect(
      overlayImmutable(submitted, original, [["disks", "*"]]),
    ).toEqual({ disks: ["a", "b"] })
  })

  it("extends submitted array when original is longer", () => {
    const submitted = { disks: ["x"] }
    const original = { disks: ["a", "b"] }
    expect(
      overlayImmutable(submitted, original, [["disks", "*"]]),
    ).toEqual({ disks: ["a", "b"] })
  })

  it("overlays nested array-item field when only that field is immutable", () => {
    const submitted = {
      disks: [
        { name: "renamed1", size: "10Gi" },
        { name: "renamed2", size: "20Gi" },
      ],
    }
    const original = {
      disks: [
        { name: "disk1", size: "5Gi" },
        { name: "disk2", size: "5Gi" },
      ],
    }
    expect(
      overlayImmutable(submitted, original, [["disks", "*", "name"]]),
    ).toEqual({
      disks: [
        { name: "disk1", size: "10Gi" },
        { name: "disk2", size: "20Gi" },
      ],
    })
  })

  it("tolerates null/undefined values along the path without blanking the user input", () => {
    expect(
      overlayImmutable({}, { storageClass: "slow" }, [["storageClass"]]),
    ).toEqual({ storageClass: "slow" })
    // Source has no value at the path: keep target's value rather than
    // erasing the field — see "keeps submitted value when … absent from original".
    expect(
      overlayImmutable({ storageClass: "fast" }, {}, [["storageClass"]]),
    ).toEqual({ storageClass: "fast" })
  })

  it("skips overlay (with warn) when the user shrunk an array with per-element-nested-immutable fields", () => {
    // Index-aligned overlay corrupts which element was deleted when the
    // length changed (it re-anchors source values onto surviving indices).
    // Skip the overlay for this path and let admission enforce the rule.
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {})
    const submitted = { disks: [{ name: "renamed", size: "10Gi" }] }
    const original = {
      disks: [
        { name: "disk1", size: "5Gi" },
        { name: "disk2", size: "5Gi" },
      ],
    }
    expect(
      overlayImmutable(submitted, original, [["disks", "*", "name"]]),
    ).toEqual({ disks: [{ name: "renamed", size: "10Gi" }] })
    expect(warn).toHaveBeenCalled()
    warn.mockRestore()
  })

  it("skips overlay (with warn) when the user deleted the first of two immutable-named entries", () => {
    // Direct repro of the index-anchored swap bug discovered in review:
    // submitted shrinks from 2 to 1 and the naive overlay would assign
    // source[0].name to the surviving entry. We bail instead.
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {})
    const submitted = { disks: [{ name: "b", size: "2Gi" }] }
    const original = {
      disks: [
        { name: "a", size: "1Gi" },
        { name: "b", size: "2Gi" },
      ],
    }
    expect(
      overlayImmutable(submitted, original, [["disks", "*", "name"]]),
    ).toEqual({ disks: [{ name: "b", size: "2Gi" }] })
    expect(warn).toHaveBeenCalled()
    warn.mockRestore()
  })

  it("warns and clones original when a top-level wildcard immutable path is supplied", () => {
    // findImmutablePaths emits [["*"]] for schemas whose root is an array
    // or map carrying the rule on items/additionalProperties. The naive
    // overlay would no-op (the loop's return value was discarded). Match
    // the root-immutable handling: warn and replace wholesale.
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {})
    const submitted = ["x", "y"]
    const original = ["a", "b"]
    expect(overlayImmutable(submitted, original, [["*"]])).toEqual(original)
    expect(warn).toHaveBeenCalled()
    warn.mockRestore()
  })

  it("freezes a whole additionalProperties map when * is the last segment", () => {
    // The schema declares every value in the map immutable. We treat this as
    // whole-map immutable: defence-in-depth must reject mutations from the
    // YAML editor or devtools, not just the form.
    type LabelsObj = { labels: Record<string, string> }
    const submitted: LabelsObj = {
      labels: { env: "prod-changed", added: "new" },
    }
    const original: LabelsObj = {
      labels: { env: "prod", legacy: "v1" },
    }
    expect(
      overlayImmutable(submitted, original, [["labels", "*"]]),
    ).toEqual({ labels: { env: "prod", legacy: "v1" } })
  })

  it("freezes the whole map even when only a nested value subkey carries the rule", () => {
    // Previously the overlay merged per-entry for *-not-last on a map,
    // which let a YAML-editor bypass add new keys that the form would
    // have refused. The UI marks the whole map ui:disabled; the overlay
    // now matches: drop added keys, reinstate removed keys, reset every
    // value to source. Different-content semantics (allow add) would need
    // both sides to change together — see PR description's UX trade-off.
    type Entry = { value: string; note: string }
    type LabelsObj = { labels: Record<string, Entry> }
    const submitted: LabelsObj = {
      labels: {
        env: { value: "changed", note: "edit" },
        new: { value: "novel", note: "added" },
      },
    }
    const original: LabelsObj = {
      labels: {
        env: { value: "prod", note: "orig" },
        legacy: { value: "v1", note: "old" },
      },
    }
    expect(
      overlayImmutable(submitted, original, [["labels", "*", "value"]]),
    ).toEqual({
      labels: {
        env: { value: "prod", note: "orig" },
        legacy: { value: "v1", note: "old" },
      },
    })
  })

  it("does NOT materialise an immutable leaf when its ancestor is missing in target (pinned current behaviour)", () => {
    // Tracked in cozystack/cozystack-ui#9.
    // Defence-in-depth gap: if the YAML editor strips the parent object
    // entirely, overlay cannot reach the leaf to copy the original value.
    // Pin behaviour so a future contributor fixing the gap notices the
    // test and revises it.
    const submitted = { spec: {} } as Record<string, unknown>
    const original = {
      spec: { backup: { storageClass: "slow" } },
    }
    const result = overlayImmutable(submitted, original, [
      ["spec", "backup", "storageClass"],
    ])
    expect(result).toEqual({ spec: {} })
  })

  it("array reordering by the user with index-aligned overlay re-anchors source values to the new index (pinned current behaviour)", () => {
    // Tracked in cozystack/cozystack-ui#10.
    // The overlay walks by index, not by content identity. Reordering an
    // immutable array maps source[i] onto target[i]'s new value — which
    // for a per-element-name-immutable schema swaps the names back to the
    // source order. Pinned as current behaviour. Identity-based matching
    // (e.g. by a stable id field) would be a separate change.
    const submitted = {
      disks: [
        { name: "b", size: "2Gi" },
        { name: "a", size: "1Gi" },
      ],
    }
    const original = {
      disks: [
        { name: "a", size: "1Gi" },
        { name: "b", size: "2Gi" },
      ],
    }
    expect(
      overlayImmutable(submitted, original, [["disks", "*", "name"]]),
    ).toEqual({
      disks: [
        { name: "a", size: "2Gi" },
        { name: "b", size: "1Gi" },
      ],
    })
  })

  it("does not mutate inputs", () => {
    const submitted = { storageClass: "fast", arr: [1, 2] }
    const original = { storageClass: "slow", arr: [9] }
    overlayImmutable(submitted, original, [["storageClass"]])
    expect(submitted).toEqual({ storageClass: "fast", arr: [1, 2] })
    expect(original).toEqual({ storageClass: "slow", arr: [9] })
  })

  it("warns and returns a clone of original when the root path is immutable", () => {
    const warn = vi.spyOn(console, "warn").mockImplementation(() => {})
    const submitted = { foo: "x", bar: "y" }
    const original = { foo: "X", bar: "Y" }
    const result = overlayImmutable(submitted, original, [[]])
    expect(result).toEqual(original)
    expect(warn).toHaveBeenCalledTimes(1)
    expect(warn.mock.calls[0][0]).toMatch(/root-level immutable/i)
    warn.mockRestore()
  })

  it("keeps user-added array entries with their submitted (non-source-derived) name", () => {
    // User added two entries past the original length. The nested
    // immutable field has no source counterpart for those new indices,
    // so the submitted values must survive unchanged.
    const submitted = {
      disks: [
        { name: "a", size: "1Gi" },
        { name: "b", size: "2Gi" },
        { name: "c", size: "3Gi" },
      ],
    }
    const original = { disks: [{ name: "A", size: "9Gi" }] }
    expect(
      overlayImmutable(submitted, original, [["disks", "*", "name"]]),
    ).toEqual({
      disks: [
        { name: "A", size: "1Gi" },
        { name: "b", size: "2Gi" },
        { name: "c", size: "3Gi" },
      ],
    })
  })

  it("does not access prototype methods unsafely when the field is missing on both sides", () => {
    // Regression: targetObj.hasOwnProperty was used directly, tripping
    // no-prototype-builtins. Cover the path explicitly: neither side has
    // the immutable field — overlay must be a no-op without throwing.
    const submitted = { other: "x" } as Record<string, unknown>
    const original = { other: "y" } as Record<string, unknown>
    expect(
      overlayImmutable(submitted, original, [["storageClass"]]),
    ).toEqual({ other: "x" })
  })
})
